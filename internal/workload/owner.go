package workload

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Inspection 将 owner 身份和模板保存在一起供纯恢复谓词使用。
type Inspection struct {
	Baseline           maintenancev1.WorkloadBaseline
	Template           corev1.PodTemplateSpec
	ObservedGeneration int64
	AvailableReplicas  int32
}

// Hash 产生稳定摘要；Go JSON 对 map 键排序，不持久化整份对象。
func Hash(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }

// Inspect 沿 controller UID 链识别内置副本控制器；不把缺失父对象降级为独立 RS。
func Inspect(ctx context.Context, r client.Reader, p *corev1.Pod) (Inspection, error) {
	o := metav1.GetControllerOf(p)
	if o == nil {
		return Inspection{}, fmt.Errorf("RecoveryUnknown: unmanaged pod")
	}
	key := client.ObjectKey{Namespace: p.Namespace, Name: o.Name}
	var obj client.Object
	var template corev1.PodTemplateSpec
	var desired int32 = 1
	var minReady int32
	var observed int64
	var available int32
	var rsUID types.UID
	var revision string
	var start int32
	kind := o.Kind
	switch {
	case kind == "ReplicaSet" && o.APIVersion == "apps/v1":
		rs := &appsv1.ReplicaSet{}
		if err := r.Get(ctx, key, rs); err != nil {
			return Inspection{}, err
		}
		if rs.UID != o.UID {
			return Inspection{}, fmt.Errorf("OwnerUIDChanged")
		}
		if parent := metav1.GetControllerOf(rs); parent != nil {
			if parent.Kind != "Deployment" || parent.APIVersion != "apps/v1" {
				return Inspection{}, fmt.Errorf("RecoveryUnknown: ReplicaSet parent %s", parent.Kind)
			}
			d := &appsv1.Deployment{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: parent.Name}, d); err != nil {
				return Inspection{}, err
			}
			if d.UID != parent.UID {
				return Inspection{}, fmt.Errorf("OwnerUIDChanged")
			}
			list := &appsv1.ReplicaSetList{}
			if err := r.List(ctx, list, client.InNamespace(p.Namespace)); err != nil {
				return Inspection{}, err
			}
			for i := range list.Items {
				other := &list.Items[i]
				owner := metav1.GetControllerOf(other)
				if owner != nil && owner.UID == d.UID && other.UID != rs.UID && ((other.Spec.Replicas != nil && *other.Spec.Replicas > 0) || other.Status.Replicas > 0) {
					return Inspection{}, fmt.Errorf("WorkloadUnstable: multiple active ReplicaSets")
				}
			}
			obj = d
			kind = "Deployment"
			template = d.Spec.Template
			desired = replicas(d.Spec.Replicas)
			minReady = d.Spec.MinReadySeconds
			observed = d.Status.ObservedGeneration
			available = d.Status.AvailableReplicas
			rsUID = rs.UID
		} else {
			obj = rs
			template = rs.Spec.Template
			desired = replicas(rs.Spec.Replicas)
			minReady = rs.Spec.MinReadySeconds
			observed = rs.Status.ObservedGeneration
			available = rs.Status.AvailableReplicas
		}
	case kind == "ReplicationController" && o.APIVersion == "v1":
		rc := &corev1.ReplicationController{}
		if err := r.Get(ctx, key, rc); err != nil {
			return Inspection{}, err
		}
		if rc.UID != o.UID || rc.Spec.Template == nil {
			return Inspection{}, fmt.Errorf("InvalidReplicationController")
		}
		obj = rc
		template = *rc.Spec.Template
		desired = replicas(rc.Spec.Replicas)
		minReady = rc.Spec.MinReadySeconds
		observed = rc.Status.ObservedGeneration
		available = rc.Status.AvailableReplicas
	case kind == "StatefulSet" && o.APIVersion == "apps/v1":
		s := &appsv1.StatefulSet{}
		if err := r.Get(ctx, key, s); err != nil {
			return Inspection{}, err
		}
		if s.UID != o.UID {
			return Inspection{}, fmt.Errorf("OwnerUIDChanged")
		}
		if s.Spec.PodManagementPolicy != appsv1.OrderedReadyPodManagement || s.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType || s.Status.CurrentRevision != s.Status.UpdateRevision {
			return Inspection{}, fmt.Errorf("RecoveryUnknown: unstable StatefulSet strategy")
		}
		if u := s.Spec.UpdateStrategy.RollingUpdate; u != nil && u.Partition != nil && *u.Partition != 0 {
			return Inspection{}, fmt.Errorf("RecoveryUnknown: partition")
		}
		obj = s
		template = s.Spec.Template
		desired = replicas(s.Spec.Replicas)
		minReady = s.Spec.MinReadySeconds
		observed = s.Status.ObservedGeneration
		available = s.Status.AvailableReplicas
		revision = s.Status.CurrentRevision
		if s.Spec.Ordinals != nil {
			start = s.Spec.Ordinals.Start
		}
	default:
		return Inspection{}, fmt.Errorf("RecoveryUnknown: %s/%s", o.APIVersion, o.Kind)
	}
	if obj.GetDeletionTimestamp() != nil {
		return Inspection{}, fmt.Errorf("WorkloadDeleting")
	}
	if BypassesCordon(&template.Spec) {
		return Inspection{}, fmt.Errorf("IsolationUnknown: workload bypasses cordon")
	}
	if observed < obj.GetGeneration() {
		return Inspection{}, fmt.Errorf("WorkloadUnstable: generation not observed")
	}
	b := maintenancev1.WorkloadBaseline{ObjectIdentity: maintenancev1.ObjectIdentity{Kind: kind, Namespace: p.Namespace, Name: obj.GetName(), UID: obj.GetUID()}, Generation: obj.GetGeneration(), Desired: desired, TemplateHash: Hash(template), ReplicaSetUID: rsUID, Revision: revision, StartOrdinal: start, MinReadySeconds: minReady}
	return Inspection{Baseline: b, Template: template, ObservedGeneration: observed, AvailableReplicas: available}, nil
}

func replicas(v *int32) int32 {
	if v == nil {
		return 1
	}
	return *v
}

// Ordinal 解析名字后缀，随后仍需 owner UID 与期望区间校验。
func Ordinal(p *corev1.Pod) (int32, error) {
	i := strings.LastIndex(p.Name, "-")
	if i < 0 {
		return 0, fmt.Errorf("InvalidOrdinal")
	}
	n, e := strconv.ParseInt(p.Name[i+1:], 10, 32)
	return int32(n), e
}

// MatchesOwner 使用 UID 归属而不是 label 猜测工作负载身份。
func MatchesOwner(p *corev1.Pod, b maintenancev1.WorkloadBaseline) bool {
	o := metav1.GetControllerOf(p)
	if o == nil {
		return false
	}
	if b.Kind == "Deployment" {
		return o.Kind == "ReplicaSet" && o.UID == b.ReplicaSetUID
	}
	return o.Kind == b.Kind && o.UID == b.UID
}

// Healthy 返回健康容量、连续窗口指纹及失败原因；指纹包含 UID 和重启计数。
func Healthy(in Inspection, pods []corev1.Pod, nodes map[string]*corev1.Node) (bool, string, string) {
	if in.AvailableReplicas < in.Baseline.Desired {
		return false, "", "ControllerNotAvailable"
	}
	count := int32(0)
	identities := []string{}
	for i := range pods {
		p := &pods[i]
		if p.Namespace != in.Baseline.Namespace || !MatchesOwner(p, in.Baseline) {
			continue
		}
		if Terminal(p) {
			continue
		}
		if p.DeletionTimestamp != nil {
			return false, "", "TerminatingReplica"
		}
		if !PodReady(p) {
			return false, "", "ReplicaNotReady"
		}
		n := nodes[p.Spec.NodeName]
		if n == nil || !NodeReady(n) {
			return false, "", "ReplicaNodeNotReady"
		}
		if in.Baseline.Kind == "StatefulSet" && p.Labels[appsv1.StatefulSetRevisionLabel] != in.Baseline.Revision {
			return false, "", "RevisionMismatch"
		}
		if !templateMatches(in.Template, p) {
			return false, "", "TemplateMismatch"
		}
		finger := string(p.UID)
		for _, s := range append(append([]corev1.ContainerStatus{}, p.Status.ContainerStatuses...), p.Status.InitContainerStatuses...) {
			finger += fmt.Sprintf("/%s:%d", s.Name, s.RestartCount)
		}
		identities = append(identities, finger)
		count++
	}
	if count != in.Baseline.Desired {
		return false, "", fmt.Sprintf("HealthyReplicas=%d Desired=%d", count, in.Baseline.Desired)
	}
	sort.Strings(identities)
	return true, Hash(identities), "HealthyCapacity"
}

// templateMatches 比较模板声明的字段，忽略绑定、默认值和额外注入字段。
// 列表按名称匹配，避免注入 sidecar 或 service-account 卷改变列表位置。
func templateMatches(template corev1.PodTemplateSpec, pod *corev1.Pod) bool {
	if !equality.Semantic.DeepDerivative(template.Labels, pod.Labels) || !equality.Semantic.DeepDerivative(template.Annotations, pod.Annotations) {
		return false
	}
	containersMatch := func(expected, actual []corev1.Container) bool {
		for _, e := range expected {
			found := false
			for _, a := range actual {
				if e.Name == a.Name && equality.Semantic.DeepDerivative(e, a) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	if !containersMatch(template.Spec.Containers, pod.Spec.Containers) || !containersMatch(template.Spec.InitContainers, pod.Spec.InitContainers) {
		return false
	}
	for _, e := range template.Spec.Volumes {
		found := false
		for _, a := range pod.Spec.Volumes {
			if e.Name == a.Name && equality.Semantic.DeepDerivative(e, a) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, e := range template.Spec.Tolerations {
		found := false
		for _, a := range pod.Spec.Tolerations {
			if equality.Semantic.DeepEqual(e, a) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	expected := template.Spec.DeepCopy()
	expected.Containers, expected.InitContainers, expected.Volumes, expected.Tolerations = nil, nil, nil, nil
	return equality.Semantic.DeepDerivative(*expected, pod.Spec)
}

// MatchingPDB 只检查存在性，预算必须交给 Eviction API。
func MatchingPDB(ctx context.Context, r client.Reader, p *corev1.Pod) (bool, error) {
	list := &policyv1.PodDisruptionBudgetList{}
	if err := r.List(ctx, list, client.InNamespace(p.Namespace)); err != nil {
		return false, err
	}
	for _, b := range list.Items {
		if b.Spec.Selector == nil {
			continue
		}
		s, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
		if err != nil {
			return false, err
		}
		if s.Matches(labels.Set(p.Labels)) {
			return true, nil
		}
	}
	return false, nil
}

// Reload 从持久基线找到控制器，旧 Pod 消失后仍能继续恢复检查。
func Reload(ctx context.Context, r client.Reader, b maintenancev1.WorkloadBaseline) (Inspection, error) {
	ref := metav1.OwnerReference{APIVersion: "apps/v1", Kind: b.Kind, Name: b.Name, UID: b.UID}
	if b.Kind == "ReplicationController" {
		ref.APIVersion = "v1"
	}
	if b.Kind == "Deployment" {
		list := &appsv1.ReplicaSetList{}
		if err := r.List(ctx, list, client.InNamespace(b.Namespace)); err != nil {
			return Inspection{}, err
		}
		found := false
		for _, rs := range list.Items {
			if rs.UID == b.ReplicaSetUID {
				ref.Kind = "ReplicaSet"
				ref.Name = rs.Name
				ref.UID = rs.UID
				found = true
				break
			}
		}
		if !found {
			return Inspection{}, fmt.Errorf("WorkloadChanged: active ReplicaSet missing")
		}
	}
	controller := true
	ref.Controller = &controller
	in, err := Inspect(ctx, r, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: b.Namespace, OwnerReferences: []metav1.OwnerReference{ref}}})
	if err != nil {
		return Inspection{}, err
	}
	if Hash(in.Baseline) != Hash(b) {
		return Inspection{}, fmt.Errorf("WorkloadChanged: %s/%s", b.Kind, b.Name)
	}
	return in, nil
}
