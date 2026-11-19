package controller

import (
	"context"
	"fmt"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/maintenance"
	"github.com/smartdrain/smartdrain/internal/observability"
	"github.com/smartdrain/smartdrain/internal/workload"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// advanceOperation 只推进持久记录中的原 UID，恢复检查不替代删除观察。
func (r *NodeMaintenanceReconciler) advanceOperation(ctx context.Context, t *maintenancev1.NodeMaintenance) (ctrl.Result, error) {
	op := t.Status.CurrentOperation
	gone, p, err := r.originalGone(ctx, op)
	if err != nil {
		return ctrl.Result{}, err
	}
	if gone {
		op.State = "Gone"
		op.UnresolvedUnknown = false
		if t.Spec.ExecutionPolicy == maintenancev1.PolicyGuarded && op.RecoveryKind != "PodGone" {
			s, err := r.snapshot(ctx, t)
			if err != nil {
				return ctrl.Result{}, err
			}
			if why := r.operationRecovered(ctx, t, s); why != "" {
				if r.expired(t) {
					t.Status.Phase = "Failed"
				}
				return r.wait(ctx, t, "WorkloadRecovered", why)
			}
		}
		t.Status.CompletedOperations++
		t.Status.CurrentOperation = nil
		return r.save(ctx, t, time.Millisecond)
	}
	if p.DeletionTimestamp != nil {
		if r.expired(t) {
			t.Status.Phase = "Failed"
		}
		op.Accepted = true
		op.State = "Accepted"
		if t.Spec.Drain.SkipWaitForDeleteTimeoutSeconds > 0 && r.now().Sub(p.DeletionTimestamp.Time) > time.Duration(t.Spec.Drain.SkipWaitForDeleteTimeoutSeconds)*time.Second {
			if len(t.Status.PendingCleanup) >= maintenancev1.MaxRecords {
				return r.wait(ctx, t, "Blocked", "CleanupCapacityExceeded")
			}
			t.Status.PendingCleanup = append(t.Status.PendingCleanup, *op)
			t.Status.CompletedOperations++
			t.Status.CurrentOperation = nil
			return r.save(ctx, t, time.Millisecond)
		}
		return r.wait(ctx, t, "Blocked", "WaitingForOriginalPodDeletion")
	}
	if op.Accepted {
		if r.expired(t) {
			t.Status.Phase = "Failed"
		}
		return r.wait(ctx, t, "Blocked", "AcceptedRequestAwaitingDeletion")
	}
	if op.State == "AttemptStarted" {
		op.UnresolvedUnknown = true
		op.State = "Unknown"
		return r.save(ctx, t, time.Millisecond)
	}
	if t.Spec.Paused {
		return r.wait(ctx, t, "Paused", "UserPaused")
	}
	if r.expired(t) || t.Status.Phase == "Failed" {
		t.Status.Phase = "Failed"
		return r.wait(ctx, t, "Blocked", "DeadlineExceeded")
	}
	if op.RetryAfter != nil && r.now().Before(op.RetryAfter.Time) {
		return r.wait(ctx, t, "Blocked", "SubmissionBackoff")
	}
	s, err := r.snapshot(ctx, t)
	if err != nil {
		return ctrl.Result{}, err
	}
	why, err := r.assess(ctx, t, s, false)
	if err != nil {
		return ctrl.Result{}, err
	}
	if why != "" {
		return r.wait(ctx, t, "Blocked", why)
	}
	if report := workload.Classify(ctx, r.APIReader, p, t.Spec, r.now()); report.Action != workload.Evict {
		return r.wait(ctx, t, "Blocked", "OriginalPodNoLongerEligible: "+report.Reason)
	}
	if t.Spec.ExecutionPolicy == maintenancev1.PolicyGuarded {
		if why := r.allRecovered(ctx, t, s, false); why != "" {
			return r.wait(ctx, t, "WorkloadRecovered", why)
		}
		if err := workload.VerifyVolumes(ctx, r.APIReader, op.Volumes); err != nil {
			return r.wait(ctx, t, "Blocked", err.Error())
		}
	}
	if err := r.freshPermission(ctx, t); err != nil {
		return r.wait(ctx, t, "Blocked", err.Error())
	}
	if op.Action == "Delete" && (!r.AllowDelete || t.Spec.ExecutionPolicy != maintenancev1.PolicyCompatible) {
		return r.wait(ctx, t, "Blocked", "DirectDeleteNotEnabled")
	}
	op.State = "AttemptStarted"
	op.Attempts++
	if err := r.Status().Update(ctx, t); err != nil {
		return ctrl.Result{}, err
	}
	// API 客户端会用响应重填 t；刷新嵌套指针，不能修改写回前的旧副本。
	op = t.Status.CurrentOperation
	// WAL 持久化后再次直读许可，缩小意图变化与外部副作用之间的窗口。
	if err := r.freshPermission(ctx, t); err != nil {
		return r.wait(ctx, t, "Blocked", err.Error())
	}
	started := time.Now()
	err = r.Executor.Submit(ctx, op, t.Spec.Drain, false)
	maintenance.RecordResult(op, err)
	op.RetryAfter = nil
	if err != nil {
		delay := r.poll()
		for i := int32(1); i < op.Attempts && delay < 30*time.Second; i++ {
			delay *= 2
		}
		if delay > 30*time.Second || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			delay = 30 * time.Second
		}
		if seconds, ok := apierrors.SuggestsClientDelay(err); ok && time.Duration(seconds)*time.Second > delay {
			delay = time.Duration(seconds) * time.Second
		}
		next := metav1.NewTime(r.now().Add(delay))
		op.RetryAfter = &next
	}
	observability.Attempts.WithLabelValues(op.Action, op.State).Inc()
	observability.RequestDuration.WithLabelValues(op.Action).Observe(time.Since(started).Seconds())
	log.FromContext(ctx).Info("pod operation attempted", "nodeUID", t.Status.NodeUID, "operationID", op.ID, "podUID", op.UID, "action", op.Action, "outcome", op.State, "unresolvedUnknown", op.UnresolvedUnknown, "duration", time.Since(started))
	if err != nil {
		r.condition(t, "Blocked", metav1.ConditionTrue, "SubmissionRejectedOrUnknown", err.Error())
	}
	return r.save(ctx, t, r.poll())
}

func (r *NodeMaintenanceReconciler) observePending(ctx context.Context, t *maintenancev1.NodeMaintenance) error {
	remaining := []maintenancev1.Operation{}
	for _, op := range t.Status.PendingCleanup {
		gone, _, err := r.originalGone(ctx, &op)
		if err != nil {
			return err
		}
		if !gone {
			remaining = append(remaining, op)
		}
	}
	t.Status.PendingCleanup = remaining
	return nil
}

// planOnly 无节点写入和任务槽；服务端试请求明确携带 dryRun=All。
func (r *NodeMaintenanceReconciler) planOnly(ctx context.Context, t *maintenancev1.NodeMaintenance) (ctrl.Result, error) {
	if !t.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if t.Spec.DesiredState == "Restored" {
		t.Status.Phase = "Completed"
		return r.save(ctx, t, 0)
	}
	if t.Status.Phase == "Completed" && t.Status.ObservedGeneration == t.Generation {
		return ctrl.Result{}, nil
	}
	s, err := r.snapshot(ctx, t)
	if err != nil {
		t.Status.Phase = "Completed"
		r.condition(t, "Blocked", metav1.ConditionTrue, "PlanFailed", err.Error())
		r.condition(t, "MaintenanceReady", metav1.ConditionFalse, "PlanOnly", "No changes have been applied")
		return r.save(ctx, t, 0)
	}
	why, err := r.assess(ctx, t, s, true)
	if err != nil {
		return ctrl.Result{}, err
	}
	if t.Spec.Drain.DryRun == "Server" {
		for i := range s.targets {
			p := &s.targets[i]
			op := &maintenancev1.Operation{ObjectIdentity: workload.Identity(p), Action: "Evict"}
			if t.Spec.Drain.DisableEviction || !r.EvictionSupported {
				if !r.AllowDelete || t.Spec.ExecutionPolicy != maintenancev1.PolicyCompatible {
					why = "DirectDeleteNotEnabled"
					break
				}
				op.Action = "Delete"
			}
			if err := r.Executor.Submit(ctx, op, t.Spec.Drain, true); err != nil {
				why = fmt.Sprintf("ServerDryRun: %v", err)
				break
			}
		}
	}
	t.Status.Phase = "Completed"
	if why != "" {
		r.condition(t, "Blocked", metav1.ConditionTrue, "PlanBlocked", why)
	} else {
		r.condition(t, "Blocked", metav1.ConditionFalse, "PlanCanStart", "Snapshot only; not a reservation")
	}
	r.condition(t, "MaintenanceReady", metav1.ConditionFalse, "PlanOnly", "No changes have been applied")
	return r.save(ctx, t, 0)
}
