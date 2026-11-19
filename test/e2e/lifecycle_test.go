package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type environment struct {
	c                 client.Client
	ctx               context.Context
	namespace, source string
	t                 *testing.T
}

func setup(t *testing.T) *environment {
	t.Helper()
	if os.Getenv("SMARTDRAIN_E2E") != "1" {
		t.Skip("set SMARTDRAIN_E2E=1; see docs/experiments.md")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = maintenancev1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := fmt.Sprintf("sd-e2e-%d", time.Now().UnixNano())
	e := &environment{c: c, ctx: context.Background(), namespace: ns, source: "smartdrain-dev-worker", t: t}
	node := &corev1.Node{}
	if err := c.Get(e.ctx, client.ObjectKey{Name: e.source}, node); err != nil || node.Spec.ProviderID != "kind://docker/smartdrain-dev/smartdrain-dev-worker" {
		t.Fatalf("e2e requires the dedicated smartdrain-dev kind cluster: %v", err)
	}
	if err := c.Create(e.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.restoreAll()
		_ = c.Delete(e.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		// Namespace 删除是异步的，必须等业务 Pod 消失才进入下一个整节点案例。
		if err := wait.PollUntilContextTimeout(e.ctx, time.Second, 90*time.Second, true, func(context.Context) (bool, error) {
			return apierrors.IsNotFound(c.Get(e.ctx, client.ObjectKey{Name: ns}, &corev1.Namespace{})), nil
		}); err != nil {
			t.Errorf("namespace cleanup: %v", err)
		}
	})
	return e
}

func (e *environment) until(desc string, timeout time.Duration, predicate func() bool) {
	e.t.Helper()
	if err := wait.PollUntilContextTimeout(e.ctx, 500*time.Millisecond, timeout, true, func(context.Context) (bool, error) { return predicate(), nil }); err != nil {
		e.dump(desc)
		e.t.Fatalf("%s: %v", desc, err)
	}
}

func (e *environment) dump(name string) {
	dir := os.Getenv("SMARTDRAIN_ARTIFACTS")
	if dir == "" {
		dir = filepath.Join("..", "..", "artifacts", "runs", e.namespace)
	}
	_ = os.MkdirAll(dir, 0755)
	tasks := &maintenancev1.NodeMaintenanceList{}
	_ = e.c.List(e.ctx, tasks)
	pods := &corev1.PodList{}
	_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
	events := &corev1.EventList{}
	_ = e.c.List(e.ctx, events, client.InNamespace(e.namespace))
	nodes := &corev1.NodeList{}
	_ = e.c.List(e.ctx, nodes)
	pvcs := &corev1.PersistentVolumeClaimList{}
	_ = e.c.List(e.ctx, pvcs, client.InNamespace(e.namespace))
	pvs := &corev1.PersistentVolumeList{}
	_ = e.c.List(e.ctx, pvs)
	for suffix, value := range map[string]any{"tasks": tasks, "pods": pods, "events": events, "nodes": nodes, "pvcs": pvcs, "pvs": pvs} {
		b, _ := json.MarshalIndent(value, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, e.namespace+"-"+name+"-"+suffix+".json"), b, 0644)
	}
}

func (e *environment) cordon(name string, value bool) {
	e.t.Helper()
	n := &corev1.Node{}
	if err := e.c.Get(e.ctx, client.ObjectKey{Name: name}, n); err != nil {
		e.t.Fatal(err)
	}
	base := n.DeepCopy()
	n.Spec.Unschedulable = value
	if err := e.c.Patch(e.ctx, n, client.MergeFrom(base)); err != nil {
		e.t.Fatal(err)
	}
}

func template(labels map[string]string, delay string) corev1.PodTemplateSpec {
	grace := int64(3)
	return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{TerminationGracePeriodSeconds: &grace, Containers: []corev1.Container{{Name: "app", Image: "smartdrain-testapp:dev", ImagePullPolicy: corev1.PullIfNotPresent, Env: []corev1.EnvVar{{Name: "READY_DELAY_SECONDS", Value: delay}}, Ports: []corev1.ContainerPort{{ContainerPort: 8080}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 1}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("32Mi")}}}}}}
}

func (e *environment) deployment(name string, replicas int32, delay string) *appsv1.Deployment {
	e.cordon("smartdrain-dev-worker2", true)
	e.cordon("smartdrain-dev-worker3", true)
	labels := map[string]string{"app": name}
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: e.namespace}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template(labels, delay)}}
	if err := e.c.Create(e.ctx, d); err != nil {
		e.t.Fatal(err)
	}
	e.until("deployment-ready", 2*time.Minute, func() bool {
		_ = e.c.Get(e.ctx, client.ObjectKeyFromObject(d), d)
		return d.Status.ReadyReplicas == replicas && d.Status.AvailableReplicas == replicas
	})
	pods := &corev1.PodList{}
	_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace), client.MatchingLabels(labels))
	for _, p := range pods.Items {
		if p.Spec.NodeName != e.source {
			e.t.Fatalf("fixture on wrong node: %s", p.Spec.NodeName)
		}
	}
	e.cordon("smartdrain-dev-worker2", false)
	e.cordon("smartdrain-dev-worker3", false)
	return d
}

func (e *environment) task(name, policy string, configure ...func(*maintenancev1.NodeMaintenance)) *maintenancev1.NodeMaintenance {
	t := &maintenancev1.NodeMaintenance{ObjectMeta: metav1.ObjectMeta{Name: e.namespace + "-" + name, Labels: map[string]string{"smartdrain.dev/test-namespace": e.namespace}}, Spec: maintenancev1.NodeMaintenanceSpec{NodeName: e.source, Mode: "Execute", ExecutionPolicy: policy, DesiredState: "Maintenance", TimeoutSeconds: 180, JobPolicy: "Default", Drain: maintenancev1.DrainOptions{IgnoreDaemonSets: true, GracePeriodSeconds: -1, ChunkSize: 500, DryRun: "Client"}}}
	for _, f := range configure {
		f(t)
	}
	if err := e.c.Create(e.ctx, t); err != nil {
		e.t.Fatal(err)
	}
	return t
}

func (e *environment) getTask(t *maintenancev1.NodeMaintenance) *maintenancev1.NodeMaintenance {
	obj := &maintenancev1.NodeMaintenance{}
	if err := e.c.Get(e.ctx, client.ObjectKeyFromObject(t), obj); err != nil {
		e.t.Fatal(err)
	}
	return obj
}
func (e *environment) restore(t *maintenancev1.NodeMaintenance) {
	current := e.getTask(t)
	base := current.DeepCopy()
	current.Spec.DesiredState = "Restored"
	if err := e.c.Patch(e.ctx, current, client.MergeFrom(base)); err != nil {
		e.t.Fatal(err)
	}
	e.until("restore", time.Minute, func() bool { return e.getTask(t).Status.Phase == "Completed" })
	_ = e.c.Delete(e.ctx, current)
}
func (e *environment) restoreAll() {
	list := &maintenancev1.NodeMaintenanceList{}
	_ = e.c.List(e.ctx, list, client.MatchingLabels{"smartdrain.dev/test-namespace": e.namespace})
	for i := range list.Items {
		t := &list.Items[i]
		base := t.DeepCopy()
		t.Spec.DesiredState = "Restored"
		_ = e.c.Patch(e.ctx, t, client.MergeFrom(base))
		_ = wait.PollUntilContextTimeout(e.ctx, time.Second, time.Minute, true, func(context.Context) (bool, error) {
			current := &maintenancev1.NodeMaintenance{}
			err := e.c.Get(e.ctx, client.ObjectKeyFromObject(t), current)
			return apierrors.IsNotFound(err) || (err == nil && current.Status.Phase == "Completed"), nil
		})
		_ = e.c.Delete(e.ctx, t)
	}
	for _, name := range []string{"smartdrain-dev-worker2", "smartdrain-dev-worker3"} {
		n := &corev1.Node{}
		if e.c.Get(e.ctx, client.ObjectKey{Name: name}, n) == nil && n.Spec.Unschedulable {
			base := n.DeepCopy()
			n.Spec.Unschedulable = false
			_ = e.c.Patch(e.ctx, n, client.MergeFrom(base))
		}
	}
}

func TestGuardedDeploymentLifecycle(t *testing.T) {
	e := setup(t)
	e.deployment("web", 2, "4")
	task := e.task("guarded", maintenancev1.PolicyGuarded)
	e.until("guarded-maintenance", 3*time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "MaintenanceReady") })
	got := e.getTask(task)
	if got.Status.CompletedOperations != 2 || !meta.IsStatusConditionTrue(got.Status.Conditions, "WorkloadRecovered") {
		t.Fatalf("unexpected result: %+v", got.Status)
	}
	e.dump("guarded-complete")
	e.restore(task)
}

func TestPDBBlocksWithoutDeleteFallback(t *testing.T) {
	e := setup(t)
	e.deployment("web", 1, "0")
	min := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "strict", Namespace: e.namespace}, Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &min, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}
	if err := e.c.Create(e.ctx, pdb); err != nil {
		t.Fatal(err)
	}
	e.until("pdb-observed", time.Minute, func() bool {
		_ = e.c.Get(e.ctx, client.ObjectKeyFromObject(pdb), pdb)
		return pdb.Status.ObservedGeneration == pdb.Generation && pdb.Status.CurrentHealthy == 1
	})
	task := e.task("pdb", maintenancev1.PolicyCompatible)
	e.until("pdb-rejected", time.Minute, func() bool { op := e.getTask(task).Status.CurrentOperation; return op != nil && op.State == "Rejected" })
	got := e.getTask(task)
	if got.Status.CompletedOperations != 0 || got.Status.CurrentOperation.Accepted {
		t.Fatal("PDB bypassed")
	}
	e.dump("pdb-blocked")
	e.restore(task)
}

func TestJobWaitAndExit(t *testing.T) {
	e := setup(t)
	e.cordon("smartdrain-dev-worker2", true)
	e.cordon("smartdrain-dev-worker3", true)
	podTemplate := template(map[string]string{"app": "batch"}, "0")
	podTemplate.Spec.RestartPolicy = corev1.RestartPolicyNever
	podTemplate.Spec.Containers[0].ReadinessProbe = nil
	podTemplate.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "APP_MODE", Value: "job"}, {Name: "JOB_SECONDS", Value: "60"}}
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "batch", Namespace: e.namespace}, Spec: batchv1.JobSpec{Template: podTemplate}}
	if err := e.c.Create(e.ctx, j); err != nil {
		t.Fatal(err)
	}
	e.until("job-running", time.Minute, func() bool {
		pods := &corev1.PodList{}
		_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
		return len(pods.Items) == 1 && pods.Items[0].Status.Phase == corev1.PodRunning
	})
	e.cordon("smartdrain-dev-worker2", false)
	e.cordon("smartdrain-dev-worker3", false)
	task := e.task("job", maintenancev1.PolicyGuarded)
	e.until("job-wait", time.Minute, func() bool {
		got := e.getTask(task)
		return got.Status.NodeClaimed && got.Status.Compute.Result == "NotApplicable"
	})
	got := e.getTask(task)
	if got.Status.CurrentOperation != nil || got.Status.CompletedOperations != 0 {
		t.Fatal("Wait submitted an eviction")
	}
	e.dump("job-waiting")
	e.restore(task)
}
