package e2e

import (
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGuardedReplicaControllers(t *testing.T) {
	for _, kind := range []string{"ReplicaSet", "ReplicationController", "StatefulSet"} {
		t.Run(kind, func(t *testing.T) {
			e := setup(t)
			e.cordon("smartdrain-dev-worker2", true)
			e.cordon("smartdrain-dev-worker3", true)
			labels := map[string]string{"app": "replicas"}
			tpl := template(labels, "1")
			count := int32(2)
			metadata := metav1.ObjectMeta{Name: "replicas", Namespace: e.namespace}
			var obj client.Object
			switch kind {
			case "ReplicaSet":
				obj = &appsv1.ReplicaSet{ObjectMeta: metadata, Spec: appsv1.ReplicaSetSpec{Replicas: &count, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: tpl}}
			case "ReplicationController":
				obj = &corev1.ReplicationController{ObjectMeta: metadata, Spec: corev1.ReplicationControllerSpec{Replicas: &count, Selector: labels, Template: &tpl}}
			case "StatefulSet":
				obj = &appsv1.StatefulSet{ObjectMeta: metadata, Spec: appsv1.StatefulSetSpec{Replicas: &count, ServiceName: "replicas", Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: tpl}}
				max := intstr.FromInt32(1)
				if err := e.c.Create(e.ctx, &policyv1.PodDisruptionBudget{ObjectMeta: metadata, Spec: policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &max, Selector: &metav1.LabelSelector{MatchLabels: labels}}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.c.Create(e.ctx, obj); err != nil {
				t.Fatal(err)
			}
			e.until("replicas-source-ready", time.Minute, func() bool {
				pods := &corev1.PodList{}
				_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
				if len(pods.Items) != 2 {
					return false
				}
				for _, p := range pods.Items {
					if p.Spec.NodeName != e.source || !workload.PodReady(&p) {
						return false
					}
				}
				return true
			})
			e.cordon("smartdrain-dev-worker2", false)
			e.cordon("smartdrain-dev-worker3", false)
			task := e.task("replicas", maintenancev1.PolicyGuarded)
			e.until("replicas-maintenance", 3*time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "MaintenanceReady") })
			got := e.getTask(task)
			if got.Status.CompletedOperations != 2 || len(got.Status.Baselines) != 1 || got.Status.Baselines[0].Kind != kind {
				t.Fatalf("unexpected status: %+v", got.Status)
			}
			e.dump(kind + "-recovered")
			e.restore(task)
		})
	}
}

func TestJobNaturalCompletion(t *testing.T) {
	e := setup(t)
	e.cordon("smartdrain-dev-worker2", true)
	e.cordon("smartdrain-dev-worker3", true)
	tpl := template(map[string]string{"app": "job"}, "0")
	tpl.Spec.RestartPolicy = corev1.RestartPolicyNever
	tpl.Spec.Containers[0].ReadinessProbe = nil
	tpl.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "APP_MODE", Value: "job"}, {Name: "JOB_SECONDS", Value: "10"}}
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "natural", Namespace: e.namespace}, Spec: batchv1.JobSpec{Template: tpl}}
	if err := e.c.Create(e.ctx, j); err != nil {
		t.Fatal(err)
	}
	e.until("natural-running", time.Minute, func() bool {
		pods := &corev1.PodList{}
		_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
		return len(pods.Items) == 1 && pods.Items[0].Status.Phase == corev1.PodRunning
	})
	e.cordon("smartdrain-dev-worker2", false)
	e.cordon("smartdrain-dev-worker3", false)
	task := e.task("natural", maintenancev1.PolicyGuarded)
	e.until("natural-complete", time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "MaintenanceReady") })
	got := e.getTask(task)
	if got.Status.CompletedOperations != 0 || len(got.Status.JobObservations) != 1 || got.Status.JobObservations[0].Outcome != "Succeeded" {
		t.Fatalf("natural completion not recorded: %+v", got.Status)
	}
	e.dump("job-natural-complete")
	e.restore(task)
}

func TestPlanOnlyServerDryRun(t *testing.T) {
	e := setup(t)
	e.deployment("plan", 1, "0")
	task := &maintenancev1.NodeMaintenance{ObjectMeta: metav1.ObjectMeta{Name: e.namespace + "-plan", Labels: map[string]string{"smartdrain.dev/test-namespace": e.namespace}}, Spec: maintenancev1.NodeMaintenanceSpec{NodeName: e.source, Mode: "PlanOnly", ExecutionPolicy: maintenancev1.PolicyCompatible, DesiredState: "Maintenance", TimeoutSeconds: 180, JobPolicy: "Default", Drain: maintenancev1.DrainOptions{IgnoreDaemonSets: true, GracePeriodSeconds: -1, DryRun: "Server"}}}
	if err := e.c.Create(e.ctx, task); err != nil {
		t.Fatal(err)
	}
	e.until("server-plan", time.Minute, func() bool { return e.getTask(task).Status.Phase == "Completed" })
	n := &corev1.Node{}
	_ = e.c.Get(e.ctx, client.ObjectKey{Name: e.source}, n)
	pods := &corev1.PodList{}
	_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
	if n.Spec.Unschedulable || len(pods.Items) != 1 || pods.Items[0].DeletionTimestamp != nil || len(e.getTask(task).Finalizers) > 0 {
		t.Fatal("PlanOnly changed the node or Pod")
	}
	e.dump("server-dry-run")
}
