// Package controller 将纯检查和持久副作用协议组合为声明式协调循环。
package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/maintenance"
	"github.com/smartdrain/smartdrain/internal/workload"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type stableObservation struct {
	fingerprint string
	since       time.Time
}

// NodeMaintenanceReconciler 通过窄 Executor 支持真实结果未知故障注入。
type NodeMaintenanceReconciler struct {
	client.Client
	APIReader         client.Reader
	Scheme            *runtime.Scheme
	Executor          maintenance.Executor
	Recorder          record.EventRecorder
	Namespace         string
	AllowDelete       bool
	EvictionSupported bool
	Now               func() time.Time
	PollInterval      time.Duration
	StabilityWindow   time.Duration
	mu                sync.Mutex
	stable            map[string]stableObservation
}

func (r *NodeMaintenanceReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
func (r *NodeMaintenanceReconciler) ownership() maintenance.Ownership {
	return maintenance.Ownership{Reader: r.APIReader, Writer: r.Client, Namespace: r.Namespace}
}
func (r *NodeMaintenanceReconciler) poll() time.Duration {
	if r.PollInterval > 0 {
		return r.PollInterval
	}
	return 2 * time.Second
}

// +kubebuilder:rbac:groups=maintenance.example.com,resources=nodemaintenances,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=maintenance.example.com,resources=nodemaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.example.com,resources=nodemaintenances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;nodes;persistentvolumeclaims;persistentvolumes;replicationcontrollers;namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=patch
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;replicasets;statefulsets;daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=csidrivers;csinodes,verbs=get;list;watch

// Reconcile 只在持久许可和最新身份校验通过后启动下一项副作用。
func (r *NodeMaintenanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	t := &maintenancev1.NodeMaintenance{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, t); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("taskUID", t.UID, "node", t.Spec.NodeName, "phase", t.Status.Phase))
	if t.Spec.Mode == "PlanOnly" {
		return r.planOnly(ctx, t)
	}
	if t.Spec.DesiredState == "Restored" || !t.DeletionTimestamp.IsZero() {
		return r.restore(ctx, t)
	}
	if t.Status.Phase == "Completed" {
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(t, maintenancev1.Finalizer) {
		base := t.DeepCopy()
		controllerutil.AddFinalizer(t, maintenancev1.Finalizer)
		return ctrl.Result{RequeueAfter: time.Millisecond}, r.Patch(ctx, t, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	}
	if t.Status.CurrentOperation != nil {
		return r.advanceOperation(ctx, t)
	}
	if t.Spec.Paused {
		return r.wait(ctx, t, "Paused", "UserPaused")
	}
	if r.expired(t) {
		t.Status.Phase = "Failed"
		return r.wait(ctx, t, "Blocked", "DeadlineExceeded")
	}
	if t.Status.Phase == "Failed" {
		return r.wait(ctx, t, "Blocked", "TaskFailedRequiresRestore")
	}
	ok, started, err := r.ownership().Acquire(ctx, t, r.now())
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		t.Status.Phase = "Pending"
		return r.wait(ctx, t, "Blocked", "TaskSlotBusy")
	}
	if t.Status.StartedAt == nil {
		at := metav1.NewTime(started)
		t.Status.StartedAt = &at
		if t.Spec.TimeoutSeconds > 0 {
			deadline := metav1.NewTime(started.Add(time.Duration(t.Spec.TimeoutSeconds) * time.Second))
			t.Status.Deadline = &deadline
		}
		t.Status.JobPolicy = t.Spec.EffectiveJobPolicy()
		t.Status.Phase = "Preparing"
		log.FromContext(ctx).Info("maintenance policy selected", "policy", t.Spec.ExecutionPolicy, "jobPolicy", t.Status.JobPolicy, "drainOptions", t.Spec.Drain)
		return r.save(ctx, t, time.Millisecond)
	}
	n := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: t.Spec.NodeName}, n); err != nil {
		return r.wait(ctx, t, "Blocked", "NodeLookup: "+err.Error())
	}
	if t.Status.NodeUID == "" {
		t.Status.NodeUID = n.UID
		return r.save(ctx, t, time.Millisecond)
	}
	if t.Status.NodeUID != n.UID {
		return r.wait(ctx, t, "Blocked", "NodeReplaced")
	}
	s, err := r.snapshot(ctx, t)
	if err != nil {
		return r.wait(ctx, t, "Blocked", err.Error())
	}
	why, err := r.assess(ctx, t, s, t.Spec.ExecutionPolicy == maintenancev1.PolicyGuarded)
	if err != nil {
		return ctrl.Result{}, err
	}
	if why != "" {
		return r.wait(ctx, t, "Blocked", why)
	}
	if !t.Status.NodeClaimed {
		if t.Status.NodeClaimStarted {
			c, e := maintenance.ReadClaim(n)
			// Node 版本完全未变证明 claim 写入尚未生效，允许重放准备后的首次 patch。
			unchanged := c == nil && t.Status.NodeClaimResourceVersion != "" && n.ResourceVersion == t.Status.NodeClaimResourceVersion
			if e != nil || (!unchanged && (c == nil || c.TaskUID != t.UID)) {
				return r.wait(ctx, t, "Blocked", "ClaimOutcomeUnknown")
			}
		}
		if !t.Status.NodeClaimStarted {
			t.Status.NodeClaimStarted = true
			t.Status.NodeClaimResourceVersion = n.ResourceVersion
			if err := r.Status().Update(ctx, t); err != nil {
				return ctrl.Result{}, err
			}
		}
		if err := r.ownership().Cordon(ctx, t); err != nil {
			// 明确拒绝可重新准备；网络错误保留意图，避免覆盖未知归属。
			if apierrors.IsConflict(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsInvalid(err) {
				t.Status.NodeClaimStarted = false
			}
			return r.wait(ctx, t, "Blocked", err.Error())
		}
		t.Status.NodeClaimed = true
		t.Status.Phase = "Draining"
		return r.save(ctx, t, time.Millisecond)
	}
	if err := r.ownership().Permit(ctx, t); err != nil {
		return r.wait(ctx, t, "Blocked", err.Error())
	}
	r.condition(t, "Blocked", metav1.ConditionFalse, "ChecksPassed", "")
	r.condition(t, "Paused", metav1.ConditionFalse, "Active", "")
	if len(s.targets) > 0 {
		if t.Spec.ExecutionPolicy == maintenancev1.PolicyGuarded {
			if why := r.allRecovered(ctx, t, s, false); why != "" {
				return r.wait(ctx, t, "WorkloadRecovered", why)
			}
		}
		p := s.targets[0]
		op := &maintenancev1.Operation{ObjectIdentity: workload.Identity(&p), ID: fmt.Sprintf("%s-%d", t.UID, t.Status.CompletedOperations+1), Action: "Evict", State: "Prepared", RecoveryKind: "PodGone"}
		if t.Spec.Drain.DisableEviction || !r.EvictionSupported {
			if !r.AllowDelete || t.Spec.ExecutionPolicy != maintenancev1.PolicyCompatible {
				return r.wait(ctx, t, "Blocked", "DirectDeleteNotEnabled")
			}
			op.Action = "Delete"
		}
		if t.Spec.ExecutionPolicy == maintenancev1.PolicyGuarded && !workload.Terminal(&p) {
			owner := metav1.GetControllerOf(&p)
			if owner == nil || owner.Kind != "Job" || owner.APIVersion != "batch/v1" {
				in, e := workload.Inspect(ctx, r.APIReader, &p)
				if e != nil {
					return r.wait(ctx, t, "Blocked", e.Error())
				}
				op.OwnerUID = in.Baseline.UID
				op.RecoveryKind = "HealthyCapacity"
				if in.Baseline.Kind == "StatefulSet" {
					op.RecoveryKind = "StatefulOrdinalReady"
					op.Ordinal, _ = workload.Ordinal(&p)
				}
			}
			storage, e := workload.AnalyzeStorage(ctx, r.APIReader, &p, s.nodes)
			if e != nil {
				return r.wait(ctx, t, "Blocked", e.Error())
			}
			op.Volumes = storage.Volumes
		}
		t.Status.CurrentOperation = op
		t.Status.Phase = "Draining"
		r.condition(t, "MaintenanceReady", metav1.ConditionFalse, "Draining", "")
		if op.RecoveryKind != "PodGone" {
			r.condition(t, "WorkloadRecovered", metav1.ConditionFalse, "OperationPrepared", "Waiting for old UID termination and replacement recovery")
		}
		result, err := r.save(ctx, t, time.Millisecond)
		if err == nil {
			log.FromContext(ctx).Info("pod operation prepared", "nodeUID", t.Status.NodeUID, "operationID", op.ID, "podUID", op.UID, "compute", t.Status.Compute, "storage", t.Status.Storage, "recovery", t.Status.Recovery)
		}
		return result, err
	}
	if s.waiting > 0 {
		return r.wait(ctx, t, "Blocked", "WaitingForLocalJobs")
	}
	r.condition(t, "DrainComplete", metav1.ConditionTrue, "SelectedTargetsHandled", "")
	if s.activeLeft > 0 || len(t.Status.PendingCleanup) > 0 {
		t.Status.Phase = "Holding"
		r.condition(t, "SourceDrained", metav1.ConditionFalse, "RemainingPods", "")
		if err := r.observePending(ctx, t); err != nil {
			return ctrl.Result{}, err
		}
		return r.wait(ctx, t, "Blocked", "SelectedScopeOrSkippedPods")
	}
	r.condition(t, "SourceDrained", metav1.ConditionTrue, "NoActiveBusinessPods", "")
	if t.Spec.ExecutionPolicy == maintenancev1.PolicyGuarded {
		if why := r.allRecovered(ctx, t, s, true); why != "" {
			return r.wait(ctx, t, "WorkloadRecovered", why)
		}
		if len(t.Status.Baselines) > 0 {
			r.condition(t, "WorkloadRecovered", metav1.ConditionTrue, "RecoveryVerified", "")
		} else {
			r.condition(t, "WorkloadRecovered", metav1.ConditionUnknown, "NotApplicable", "No replica recovery target; Job instance outcomes are reported separately")
		}
	} else {
		r.condition(t, "WorkloadRecovered", metav1.ConditionUnknown, "CompatibilityPolicy", "Draining does not assert application recovery")
	}
	t.Status.Phase = "Maintenance"
	r.condition(t, "MaintenanceReady", metav1.ConditionTrue, "PolicySatisfied", "")
	return r.save(ctx, t, r.poll())
}

func (r *NodeMaintenanceReconciler) expired(t *maintenancev1.NodeMaintenance) bool {
	return t.Status.Deadline != nil && t.Status.Phase != "Maintenance" && t.Status.Phase != "Holding" && !r.now().Before(t.Status.Deadline.Time)
}

func (r *NodeMaintenanceReconciler) condition(t *maintenancev1.NodeMaintenance, kind string, status metav1.ConditionStatus, reason, message string) {
	if message == "" {
		message = reason
	}
	if len(message) > 2048 {
		message = message[:2048]
	}
	meta.SetStatusCondition(&t.Status.Conditions, metav1.Condition{Type: kind, Status: status, Reason: reasonToken(reason), Message: message, ObservedGeneration: t.Generation, LastTransitionTime: metav1.NewTime(r.now())})
}

func reasonToken(s string) string {
	if i := strings.IndexAny(s, ": =/"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		s = "Waiting"
	}
	var token strings.Builder
	for _, ch := range s {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
			token.WriteRune(ch)
		} else {
			token.WriteByte('_')
		}
		if token.Len() >= 96 {
			break
		}
	}
	out := token.String()
	if out[0] < 'A' || (out[0] > 'Z' && out[0] < 'a') || out[0] > 'z' {
		out = "Reason_" + out
	}
	return out
}

func (r *NodeMaintenanceReconciler) save(ctx context.Context, t *maintenancev1.NodeMaintenance, after time.Duration) (ctrl.Result, error) {
	t.Status.ObservedGeneration = t.Generation
	err := r.Status().Update(ctx, t)
	return ctrl.Result{RequeueAfter: after}, err
}

func (r *NodeMaintenanceReconciler) wait(ctx context.Context, t *maintenancev1.NodeMaintenance, kind, why string) (ctrl.Result, error) {
	status := metav1.ConditionTrue
	if kind == "WorkloadRecovered" {
		status = metav1.ConditionFalse
	}
	previous := meta.FindStatusCondition(t.Status.Conditions, kind)
	if (previous == nil || previous.Message != why) && r.Recorder != nil {
		r.Recorder.Event(t, corev1.EventTypeNormal, reasonToken(why), why)
	}
	r.condition(t, kind, status, why, why)
	r.condition(t, "MaintenanceReady", metav1.ConditionFalse, why, why)
	log.FromContext(ctx).V(1).Info("waiting", "reason", why)
	return r.save(ctx, t, r.poll())
}

// freshPermission 防止暂停、退出或超时在准备记录之后到达时仍提交删除。
func (r *NodeMaintenanceReconciler) freshPermission(ctx context.Context, t *maintenancev1.NodeMaintenance) error {
	fresh := &maintenancev1.NodeMaintenance{}
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(t), fresh); err != nil {
		return err
	}
	if fresh.UID != t.UID || fresh.Spec.Paused || fresh.Spec.DesiredState == "Restored" || !fresh.DeletionTimestamp.IsZero() || r.expired(fresh) {
		return fmt.Errorf("ActionPermissionChanged")
	}
	if fresh.Status.CurrentOperation == nil || t.Status.CurrentOperation == nil || fresh.Status.CurrentOperation.ID != t.Status.CurrentOperation.ID {
		return fmt.Errorf("OperationChanged")
	}
	return r.ownership().Permit(ctx, fresh)
}

// originalGone 用 UID 判断旧对象是否消失，同名新对象不能成为重试目标。
func (r *NodeMaintenanceReconciler) originalGone(ctx context.Context, op *maintenancev1.Operation) (bool, *corev1.Pod, error) {
	p := &corev1.Pod{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: op.Namespace, Name: op.Name}, p)
	if apierrors.IsNotFound(err) {
		return true, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return p.UID != op.UID, p, nil
}
