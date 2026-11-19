package controller

import (
	"context"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerpkg "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SetupWithManager 监听状态变化；全局仅一项执行任务，避免构建不必要的通用索引系统。
func (r *NodeMaintenanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapChange := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		tasks := &maintenancev1.NodeMaintenanceList{}
		if err := r.List(ctx, tasks); err != nil {
			return nil
		}
		out := []reconcile.Request{}
		for _, t := range tasks.Items {
			if t.Spec.Mode != "PlanOnly" && t.Status.Phase != "Completed" {
				out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&t)})
			}
		}
		return out
	})
	deleting := predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		return (e.ObjectOld.GetDeletionTimestamp() == nil) != (e.ObjectNew.GetDeletionTimestamp() == nil)
	}}
	b := ctrl.NewControllerManagedBy(mgr).For(&maintenancev1.NodeMaintenance{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}, deleting))).WithOptions(controllerpkg.Options{MaxConcurrentReconciles: 1})
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.Node{}, &appsv1.Deployment{}, &appsv1.ReplicaSet{}, &appsv1.StatefulSet{}, &appsv1.DaemonSet{}, &corev1.ReplicationController{}, &batchv1.Job{}, &batchv1.CronJob{}, &policyv1.PodDisruptionBudget{}, &corev1.PersistentVolumeClaim{}, &corev1.PersistentVolume{}, &storagev1.CSINode{}, &storagev1.CSIDriver{}} {
		b = b.Watches(obj, mapChange)
	}
	return b.Complete(r)
}
