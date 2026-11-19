package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRealStaticPodCronJobAndUnknownOwner(t *testing.T) {
	if os.Getenv("SMARTDRAIN_MIXED_TESTS") != "1" {
		t.Skip("explicit real static-Pod fixture required")
	}
	e := setup(t)
	run := func(args ...string) {
		if out, err := exec.CommandContext(e.ctx, "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker fixture: %v: %s", err, out)
		}
	}
	tpl := template(map[string]string{"app": "static"}, "0")
	static := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "smartdrain-static", Namespace: e.namespace}, Spec: tpl.Spec}
	data, err := json.Marshal(static)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "static.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	run("exec", e.source, "mkdir", "-p", "/etc/kubernetes/manifests")
	run("cp", path, e.source+":/etc/kubernetes/manifests/smartdrain-e2e.yaml")
	t.Cleanup(func() { run("exec", e.source, "rm", "-f", "/etc/kubernetes/manifests/smartdrain-e2e.yaml") })
	e.until("real-mirror-present", time.Minute, func() bool {
		pods := &corev1.PodList{}
		_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
		for _, p := range pods.Items {
			if p.Annotations[corev1.MirrorPodAnnotationKey] != "" && p.Status.Phase == corev1.PodRunning {
				return true
			}
		}
		return false
	})
	e.cordon("smartdrain-dev-worker2", true)
	e.cordon("smartdrain-dev-worker3", true)
	for _, kind := range []string{"deployment", "rs", "rc", "sts"} {
		labels := map[string]string{"app": "mixed-" + kind, "smartdrain.dev/mixed": "true"}
		podTemplate := template(labels, "0")
		metadata := metav1.ObjectMeta{Name: "mixed-" + kind, Namespace: e.namespace}
		one := int32(1)
		selector := &metav1.LabelSelector{MatchLabels: labels}
		var obj client.Object
		switch kind {
		case "deployment":
			obj = &appsv1.Deployment{ObjectMeta: metadata, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: selector, Template: podTemplate}}
		case "rs":
			obj = &appsv1.ReplicaSet{ObjectMeta: metadata, Spec: appsv1.ReplicaSetSpec{Replicas: &one, Selector: selector, Template: podTemplate}}
		case "rc":
			obj = &corev1.ReplicationController{ObjectMeta: metadata, Spec: corev1.ReplicationControllerSpec{Replicas: &one, Selector: labels, Template: &podTemplate}}
		case "sts":
			obj = &appsv1.StatefulSet{ObjectMeta: metadata, Spec: appsv1.StatefulSetSpec{Replicas: &one, Selector: selector, Template: podTemplate, ServiceName: "mixed-sts"}}
		}
		if err := e.c.Create(e.ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	e.until("mixed-controllers-source-ready", time.Minute, func() bool {
		pods := &corev1.PodList{}
		_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace), client.MatchingLabels{"smartdrain.dev/mixed": "true"})
		if len(pods.Items) != 4 {
			return false
		}
		for _, p := range pods.Items {
			if p.Spec.NodeName != e.source || !workload.PodReady(&p) {
				return false
			}
		}
		return true
	})
	jobTemplate := template(map[string]string{"app": "scheduled"}, "0")
	jobTemplate.Spec.RestartPolicy = corev1.RestartPolicyNever
	jobTemplate.Spec.Containers[0].ReadinessProbe = nil
	jobTemplate.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "APP_MODE", Value: "job"}, {Name: "JOB_SECONDS", Value: "120"}}
	cron := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "scheduled", Namespace: e.namespace}, Spec: batchv1.CronJobSpec{Schedule: "* * * * *", ConcurrencyPolicy: batchv1.ForbidConcurrent, JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: jobTemplate}}}}
	if err := e.c.Create(e.ctx, cron); err != nil {
		t.Fatal(err)
	}
	e.until("cronjob-created-running-pod", 90*time.Second, func() bool {
		pods := &corev1.PodList{}
		_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace), client.MatchingLabels{"app": "scheduled"})
		return len(pods.Items) == 1 && pods.Items[0].Spec.NodeName == e.source && pods.Items[0].Status.Phase == corev1.PodRunning
	})
	e.cordon("smartdrain-dev-worker2", false)
	e.cordon("smartdrain-dev-worker3", false)
	unknownTemplate := template(map[string]string{"app": "unknown"}, "0")
	unknownTemplate.Spec.NodeName = e.source
	yes := true
	unknown := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unknown", Namespace: e.namespace, OwnerReferences: []metav1.OwnerReference{{APIVersion: "external.example.com/v1", Kind: "ExternalController", Name: "external", UID: "da05725f-35ef-4475-ab2e-b5df77181eae", Controller: &yes}}}, Spec: unknownTemplate.Spec}
	if err := e.c.Create(e.ctx, unknown); err != nil {
		t.Fatal(err)
	}
	task := e.task("mixed", maintenancev1.PolicyCompatible)
	e.until("mixed-maintenance", time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "MaintenanceReady") })
	got := e.getTask(task)
	mirror := false
	for _, p := range got.Status.Pods {
		if p.Reason == "MirrorPod" {
			mirror = true
		}
	}
	if !mirror || got.Status.CompletedOperations != 6 {
		t.Fatalf("unexpected mixed result: %+v", got.Status)
	}
	e.dump("mixed-real-static-cronjob")
	e.restore(task)
}
