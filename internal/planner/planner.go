// Package planner 实现有限的 requests 布局模型，不模拟 scheduler 打分或预留。
package planner

import (
	"fmt"
	"sort"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/workload"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	resourcehelper "k8s.io/component-helpers/resource"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
)

type budget struct {
	cpu, memory, slots int64
	unknown            bool
	node               *corev1.Node
}

// Requests 使用上游有效请求算法，覆盖 init、sidecar 与 overhead。
func Requests(p *corev1.Pod) (int64, int64) {
	r := resourcehelper.PodRequests(p, resourcehelper.PodResourcesOptions{})
	return r.Cpu().MilliValue(), r.Memory().Value()
}

// Plan 返回一份保守布局见证；贪心失败不作为无解证明。
func Plan(source string, nodes []corev1.Node, residents, targets []corev1.Pod, allowed map[string]map[string]bool) maintenancev1.CheckReport {
	result := maintenancev1.CheckReport{Result: "Pass", Reason: "LayoutFound", Placements: map[string]string{}}
	if len(targets) == 0 {
		return maintenancev1.CheckReport{Result: "NotApplicable", Reason: "NoEvictTargets"}
	}
	bs := []*budget{}
	for i := range nodes {
		n := &nodes[i]
		if n.Name == source || n.Spec.Unschedulable || !workload.NodeReady(n) {
			continue
		}
		bs = append(bs, &budget{cpu: n.Status.Allocatable.Cpu().MilliValue(), memory: n.Status.Allocatable.Memory().Value(), slots: n.Status.Allocatable.Pods().Value(), node: n})
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].node.Name < bs[j].node.Name })
	for i := range residents {
		p := &residents[i]
		if workload.Terminal(p) || p.Spec.NodeName == "" {
			continue
		}
		for _, b := range bs {
			if b.node.Name == p.Spec.NodeName {
				c, m := Requests(p)
				b.cpu -= c
				b.memory -= m
				b.slots--
				if unknownResources(p) {
					b.unknown = true
				}
			}
		}
	}
	ordered := append([]corev1.Pod{}, targets...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := &ordered[i], &ordered[j]
		ac, am := Requests(a)
		bc, bm := Requests(b)
		if ac != bc {
			return ac > bc
		}
		if am != bm {
			return am > bm
		}
		return string(a.UID) < string(b.UID)
	})
	for i := range ordered {
		p := &ordered[i]
		key := p.Namespace + "/" + p.Name
		if unknownResources(p) || unknownConstraints(p) {
			return maintenancev1.CheckReport{Result: "Unknown", Reason: "UnsupportedPodFeature", Message: key}
		}
		cpu, mem := Requests(p)
		fit := false
		uncertain := false
		individuallyPossible := false
		for _, b := range bs {
			ok, err := nodeaffinity.GetRequiredNodeAffinity(p).Match(b.node)
			if err != nil {
				uncertain = true
				continue
			}
			if !ok {
				continue
			}
			if set, exists := allowed[string(p.UID)]; exists && !set[b.node.Name] {
				continue
			}
			tolerated, finite := tolerates(p, b.node)
			if !tolerated {
				continue
			}
			// 相关既有硬反亲和可能跨节点拓扑域生效，未知时不能声称可放置。
			if b.unknown || finite || relevantAntiAffinity(p, b.node, residents, nodes) || pendingCompetitor(p, b.node, residents) {
				uncertain = true
				continue
			}
			if cpu <= b.node.Status.Allocatable.Cpu().MilliValue() && mem <= b.node.Status.Allocatable.Memory().Value() {
				individuallyPossible = true
			}
			if b.cpu >= cpu && b.memory >= mem && b.slots > 0 {
				b.cpu -= cpu
				b.memory -= mem
				b.slots--
				result.Placements[key] = b.node.Name
				fit = true
				break
			}
		}
		if !fit {
			if uncertain {
				return maintenancev1.CheckReport{Result: "Unknown", Reason: "CandidateUnknown", Message: key}
			}
			// 仅第一个目标没有可容纳的当前候选时是可直接解释的阻塞。
			if i == 0 || !individuallyPossible {
				return maintenancev1.CheckReport{Result: "Blocked", Reason: "NoNodeFits", Message: fmt.Sprintf("%s needs %dm CPU and %d bytes", key, cpu, mem)}
			}
			return maintenancev1.CheckReport{Result: "Unknown", Reason: "GreedyNoWitness", Message: key}
		}
	}
	return result
}

func tolerates(p *corev1.Pod, n *corev1.Node) (bool, bool) {
	finite := false
	for _, t := range n.Spec.Taints {
		if t.Effect != corev1.TaintEffectNoSchedule && t.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		found := false
		unlimited := false
		for _, tol := range p.Spec.Tolerations {
			if tol.ToleratesTaint(klog.Background(), &t, false) {
				found = true
				if t.Effect != corev1.TaintEffectNoExecute || tol.TolerationSeconds == nil {
					unlimited = true
				}
			}
		}
		if !found {
			return false, false
		}
		finite = finite || !unlimited
	}
	return true, finite
}

func unknownResources(p *corev1.Pod) bool {
	if p.Spec.Resources != nil || len(p.Spec.ResourceClaims) > 0 || p.Status.Resize != "" {
		return true
	}
	for _, c := range p.Status.Conditions {
		if (c.Type == corev1.PodResizePending || c.Type == corev1.PodResizeInProgress) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	for _, c := range append(append([]corev1.Container{}, p.Spec.Containers...), p.Spec.InitContainers...) {
		for name, q := range c.Resources.Requests {
			if name != corev1.ResourceCPU && name != corev1.ResourceMemory && !q.IsZero() {
				return true
			}
		}
	}
	return false
}

func unknownConstraints(p *corev1.Pod) bool {
	for _, t := range p.Spec.Tolerations {
		if t.Operator == corev1.TolerationOpLt || t.Operator == corev1.TolerationOpGt {
			return true
		}
	}
	if p.Spec.SchedulerName != "" && p.Spec.SchedulerName != corev1.DefaultSchedulerName {
		return true
	}
	if a := p.Spec.Affinity; a != nil {
		if a.PodAffinity != nil && len(a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			return true
		}
		if a.PodAntiAffinity != nil && len(a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			return true
		}
	}
	for _, c := range p.Spec.TopologySpreadConstraints {
		if c.WhenUnsatisfiable == corev1.DoNotSchedule {
			return true
		}
	}
	for _, c := range p.Spec.Containers {
		for _, port := range c.Ports {
			if port.HostPort > 0 {
				return true
			}
		}
	}
	return len(p.Spec.SchedulingGates) > 0
}

// relevantAntiAffinity 只把可能约束当前候选的既有规则标为未知。
// namespaceSelector 需要 namespace 标签；本模型不猜测其匹配结果。
func relevantAntiAffinity(target *corev1.Pod, candidate *corev1.Node, pods []corev1.Pod, nodes []corev1.Node) bool {
	for _, resident := range pods {
		if resident.Spec.NodeName == "" || workload.Terminal(&resident) || resident.Spec.Affinity == nil || resident.Spec.Affinity.PodAntiAffinity == nil {
			continue
		}
		for _, term := range resident.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			namespaceMatches := term.NamespaceSelector != nil
			if len(term.Namespaces) == 0 && term.NamespaceSelector == nil {
				namespaceMatches = target.Namespace == resident.Namespace
			}
			for _, ns := range term.Namespaces {
				namespaceMatches = namespaceMatches || ns == target.Namespace
			}
			if !namespaceMatches {
				continue
			}
			selector, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
			if err == nil && !selector.Matches(labels.Set(target.Labels)) {
				continue
			}
			// matchLabelKeys/mismatchLabelKeys 未建模；保守保留相关候选。
			for _, n := range nodes {
				if n.Name == resident.Spec.NodeName {
					x, xok := n.Labels[term.TopologyKey]
					y, yok := candidate.Labels[term.TopologyKey]
					if xok && yok && x == y {
						return true
					}
				}
			}
		}
	}
	return false
}

// pendingCompetitor 不预留未来资源；仅将可能竞争同一候选的待调度 Pod 标为未知。
func pendingCompetitor(target *corev1.Pod, candidate *corev1.Node, pods []corev1.Pod) bool {
	for i := range pods {
		p := &pods[i]
		if p.UID == target.UID || p.Spec.NodeName != "" || workload.Terminal(p) || p.DeletionTimestamp != nil || len(p.Spec.SchedulingGates) > 0 {
			continue
		}
		match, err := nodeaffinity.GetRequiredNodeAffinity(p).Match(candidate)
		tolerated, _ := tolerates(p, candidate)
		if (match || err != nil) && tolerated {
			return true
		}
	}
	return false
}
