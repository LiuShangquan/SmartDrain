package e2e

import (
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestJobWaitFailureAndTimeout(t *testing.T) {
	for _, scenario := range []string{"failure", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			e := setup(t)
			e.cordon("smartdrain-dev-worker2", true)
			e.cordon("smartdrain-dev-worker3", true)
			tpl := template(map[string]string{"app": "batch"}, "0")
			tpl.Spec.RestartPolicy = corev1.RestartPolicyNever
			tpl.Spec.Containers[0].ReadinessProbe = nil
			duration, code := "8", "1"
			if scenario == "timeout" {
				duration = "120"
				code = "0"
			}
			tpl.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "APP_MODE", Value: "job"}, {Name: "JOB_SECONDS", Value: duration}, {Name: "JOB_EXIT_CODE", Value: code}}
			zero := int32(0)
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "batch", Namespace: e.namespace}, Spec: batchv1.JobSpec{Template: tpl, BackoffLimit: &zero}}
			if err := e.c.Create(e.ctx, job); err != nil {
				t.Fatal(err)
			}
			e.until("outcome-job-running", time.Minute, func() bool {
				pods := &corev1.PodList{}
				_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
				return len(pods.Items) == 1 && pods.Items[0].Status.Phase == corev1.PodRunning
			})
			e.cordon("smartdrain-dev-worker2", false)
			e.cordon("smartdrain-dev-worker3", false)
			task := e.task("outcome", maintenancev1.PolicyGuarded, func(t *maintenancev1.NodeMaintenance) {
				if scenario == "timeout" {
					t.Spec.TimeoutSeconds = 8
				}
			})
			if scenario == "failure" {
				e.until("failed-instance-recorded", time.Minute, func() bool {
					got := e.getTask(task)
					return len(got.Status.JobObservations) == 1 && got.Status.JobObservations[0].Outcome == "Failed" && meta.IsStatusConditionTrue(got.Status.Conditions, "MaintenanceReady")
				})
			} else {
				e.until("job-task-deadline", time.Minute, func() bool { return e.getTask(task).Status.Phase == "Failed" })
				n := &corev1.Node{}
				_ = e.c.Get(e.ctx, client.ObjectKey{Name: e.source}, n)
				if !n.Spec.Unschedulable {
					t.Fatal("timeout automatically uncordoned")
				}
				got := e.getTask(task)
				base := got.DeepCopy()
				got.Spec.Paused = true
				if err := e.c.Patch(e.ctx, got, client.MergeFrom(base)); err != nil {
					t.Fatal(err)
				}
			}
			if e.getTask(task).Status.CompletedOperations != 0 {
				t.Fatal("Job Wait evicted an instance")
			}
			e.dump("job-" + scenario)
			e.restore(task)
		})
	}
}
