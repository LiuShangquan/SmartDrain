// Package workload 负责 Pod 分类、控制器身份和恢复谓词。
package workload

import (
	"context"
	"fmt"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	Evict    = "Evict"
	Wait     = "Wait"
	Retain   = "Retain"
	Block    = "Block"
	Excluded = "Excluded"
	Skip     = "Skip"
)

// Identity 提取跨重试稳定的对象身份。
func Identity(p *corev1.Pod) maintenancev1.ObjectIdentity {
	return maintenancev1.ObjectIdentity{Kind: "Pod", Namespace: p.Namespace, Name: p.Name, UID: p.UID}
}

// Terminal 判断 Pod 是否已经结束，不能用 Job 的聚合状态替代。
func Terminal(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

// Classify 按 kubectl v0.35.0 的过滤顺序分类，再叠加显式 Job 等待。
// 上游行为参考 pkg/drain/filters.go；不以恢复分析器的 Kind 覆盖作为驱逐白名单。
func Classify(ctx context.Context, r client.Reader, p *corev1.Pod, spec maintenancev1.NodeMaintenanceSpec, now time.Time) maintenancev1.PodReport {
	result := maintenancev1.PodReport{ObjectIdentity: Identity(p), Action: Evict, Reason: "ManagedPod"}
	set := func(a, why string) maintenancev1.PodReport {
		result.Action = a
		if len(why) > 2048 {
			why = why[:2048]
		}
		result.Reason = why
		return result
	}
	if spec.Drain.PodSelector != "" {
		s, err := labels.Parse(spec.Drain.PodSelector)
		if err != nil {
			return set(Block, "InvalidSelector")
		}
		if !s.Matches(labels.Set(p.Labels)) {
			return set(Excluded, "OutsideSelector")
		}
	}
	if p.DeletionTimestamp != nil && spec.Drain.SkipWaitForDeleteTimeoutSeconds > 0 && now.Sub(p.DeletionTimestamp.Time) > time.Duration(spec.Drain.SkipWaitForDeleteTimeoutSeconds)*time.Second {
		return set(Skip, "DeletionWaitSkipped")
	}
	o := metav1.GetControllerOf(p)
	if o != nil && o.Kind == "DaemonSet" && o.APIVersion == "apps/v1" && !Terminal(p) {
		ds := &appsv1.DaemonSet{}
		err := r.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: o.Name}, ds)
		if err != nil {
			if apierrors.IsNotFound(err) && spec.Drain.Force {
				result.Reason = "OrphanDaemonSetForced"
			} else {
				return set(Block, fmt.Sprintf("DaemonSetLookup: %v", err))
			}
		} else {
			if !spec.Drain.IgnoreDaemonSets {
				return set(Block, "DaemonSetRequiresIgnore")
			}
			return set(Retain, "DaemonSetResident")
		}
	}
	if _, ok := p.Annotations[corev1.MirrorPodAnnotationKey]; ok {
		return set(Retain, "MirrorPod")
	}
	// 自然等待不迁移 emptyDir，不应误用迁移数据清理开关。
	if o != nil && o.Kind == "Job" && o.APIVersion == "batch/v1" && spec.EffectiveJobPolicy() == "Wait" {
		if Terminal(p) && p.DeletionTimestamp == nil {
			return set(Retain, "LocalJob"+string(p.Status.Phase))
		}
		return set(Wait, "LocalJobRunning")
	}
	if Terminal(p) {
		return set(Evict, "TerminalPod")
	}
	for _, v := range p.Spec.Volumes {
		if v.EmptyDir != nil && !spec.Drain.DeleteEmptyDirData {
			return set(Block, "EmptyDirRequiresConsent")
		}
	}
	if o == nil {
		if !spec.Drain.Force {
			return set(Block, "UnmanagedRequiresForce")
		}
		return set(Evict, "UnmanagedForced")
	}
	return result
}

// NodeReady 是恢复和预检共用的节点健康谓词。
func NodeReady(n *corev1.Node) bool {
	if n.DeletionTimestamp != nil {
		return false
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// PodReady 不将 terminating 的 Ready Pod 当作健康容量。
func PodReady(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil || Terminal(p) {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// BypassesCordon 检查模板中的直接绑定和实际匹配 unschedulable 的容忍。
func BypassesCordon(spec *corev1.PodSpec) bool {
	if spec.NodeName != "" {
		return true
	}
	taint := corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule}
	for _, t := range spec.Tolerations {
		if t.ToleratesTaint(klog.Background(), &taint, false) {
			return true
		}
	}
	return false
}
