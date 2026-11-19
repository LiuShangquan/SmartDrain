package controller

import (
	"context"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/maintenance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// restore 优先于暂停、期限、Job 等待和新副本恢复；从不补发删除请求。
func (r *NodeMaintenanceReconciler) restore(ctx context.Context, t *maintenancev1.NodeMaintenance) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(t, maintenancev1.Finalizer) {
		if t.DeletionTimestamp.IsZero() && t.Status.Phase != "Completed" {
			t.Status.Phase = "Completed"
			return r.save(ctx, t, 0)
		}
		return ctrl.Result{}, nil
	}
	t.Status.Phase = "Restoring"
	r.condition(t, "MaintenanceReady", metav1.ConditionFalse, "Restoring", "")
	if op := t.Status.CurrentOperation; op != nil {
		if maintenance.MayHaveExecuted(op) {
			gone, _, err := r.originalGone(ctx, op)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !gone {
				return r.wait(ctx, t, "CleanupBlocked", "OriginalOperationUnresolved")
			}
		}
		t.Status.CurrentOperation = nil
		return r.save(ctx, t, time.Millisecond)
	}
	if err := r.observePending(ctx, t); err != nil {
		return ctrl.Result{}, err
	}
	if len(t.Status.PendingCleanup) > 0 {
		return r.wait(ctx, t, "CleanupBlocked", "SkippedOperationsUnresolved")
	}
	if !t.Status.NodeCleanupConfirmed {
		result, err := r.ownership().ApplyCleanup(ctx, t)
		if err != nil {
			return r.wait(ctx, t, "CleanupBlocked", err.Error())
		}
		t.Status.NodeCleanupConfirmed = true
		t.Status.CleanupResult = result
		return r.save(ctx, t, time.Millisecond)
	}
	if err := r.ownership().RemoveClaim(ctx, t); err != nil {
		return r.wait(ctx, t, "CleanupBlocked", err.Error())
	}
	if err := r.ownership().Release(ctx, t); err != nil {
		return ctrl.Result{}, err
	}
	for _, b := range t.Status.Baselines {
		r.forgetStable(string(t.UID) + "/" + string(b.UID))
	}
	if t.Status.Phase != "Completed" {
		t.Status.Phase = "Completed"
		r.condition(t, "CleanupBlocked", metav1.ConditionFalse, "CleanupConfirmed", t.Status.CleanupResult)
		if err := r.Status().Update(ctx, t); err != nil {
			return ctrl.Result{}, err
		}
	}
	base := t.DeepCopy()
	controllerutil.RemoveFinalizer(t, maintenancev1.Finalizer)
	return ctrl.Result{}, r.Patch(ctx, t, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}
