package planner

import (
	"github.com/smartdrain/smartdrain/internal/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestCapacityAndVirtualDeduction(t *testing.T) {
	nodes := []corev1.Node{*testutil.Node("a", "400m", "1Gi"), *testutil.Node("b", "400m", "1Gi")}
	large := *testutil.Pod("large", "source", "600m")
	if got := Plan("source", nodes, nil, []corev1.Pod{large}, nil); got.Result != "Blocked" {
		t.Fatalf("fragmentation: %+v", got)
	}
	targets := []corev1.Pod{*testutil.Pod("one", "source", "300m"), *testutil.Pod("two", "source", "300m"), *testutil.Pod("three", "source", "300m")}
	if got := Plan("source", nodes, nil, targets, nil); got.Result != "Unknown" || got.Reason != "GreedyNoWitness" {
		t.Fatalf("must not reuse capacity: %+v", got)
	}
	if got := Plan("source", nodes, nil, targets[:2], nil); got.Result != "Pass" || got.Placements["test/one"] == got.Placements["test/two"] {
		t.Fatalf("witness: %+v", got)
	}
}

func TestRelevantUncertaintyOnly(t *testing.T) {
	nodes := []corev1.Node{*testutil.Node("receiver", "2", "2Gi")}
	target := testutil.Pod("target", "source", "100m")
	target.Labels = map[string]string{"app": "target"}
	resident := testutil.Pod("resident", "receiver", "100m")
	resident.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "different"}}, TopologyKey: corev1.LabelHostname}}}}
	if got := Plan("source", nodes, []corev1.Pod{*resident}, []corev1.Pod{*target}, nil); got.Result != "Pass" {
		t.Fatal("unrelated anti-affinity blocked:", got)
	}
	resident.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchLabels["app"] = "target"
	if got := Plan("source", nodes, []corev1.Pod{*resident}, []corev1.Pod{*target}, nil); got.Result != "Unknown" {
		t.Fatal("relevant anti-affinity ignored:", got)
	}
	pending := testutil.Pod("pending", "", "100m")
	if got := Plan("source", nodes, []corev1.Pod{*pending}, []corev1.Pod{*target}, nil); got.Result != "Unknown" {
		t.Fatal("pending competition ignored:", got)
	}
	pending.Spec.NodeSelector = map[string]string{corev1.LabelHostname: "elsewhere"}
	if got := Plan("source", nodes, []corev1.Pod{*pending}, []corev1.Pod{*target}, nil); got.Result != "Pass" {
		t.Fatal("unrelated pending blocked:", got)
	}
	seconds := int64(300)
	nodes[0].Spec.Taints = []corev1.Taint{{Key: "temporary", Effect: corev1.TaintEffectNoExecute}}
	target.Spec.Tolerations = []corev1.Toleration{{Key: "temporary", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds}}
	if got := Plan("source", nodes, nil, []corev1.Pod{*target}, nil); got.Result != "Unknown" {
		t.Fatal("finite tolerance incorrectly proved impossible:", got)
	}
}

func TestResidentAccountingAndStorageIntersection(t *testing.T) {
	nodes := []corev1.Node{*testutil.Node("a", "1", "1Gi"), *testutil.Node("b", "1", "1Gi")}
	resident := testutil.Pod("busy", "a", "800m")
	resident.Namespace = "other"
	p := testutil.Pod("target", "source", "500m")
	got := Plan("source", nodes, []corev1.Pod{*resident}, []corev1.Pod{*p}, map[string]map[string]bool{string(p.UID): {"a": true, "b": false}})
	if got.Result != "Blocked" {
		t.Fatalf("must intersect volume and compute, got %+v", got)
	}
	resident.Status.Phase = corev1.PodSucceeded
	if got := Plan("source", nodes, []corev1.Pod{*resident}, []corev1.Pod{*p}, nil); got.Result != "Pass" {
		t.Fatal(got)
	}
}

func TestEffectiveRequestsSidecarInitOverhead(t *testing.T) {
	p := testutil.Pod("p", "source", "100m")
	always := corev1.ContainerRestartPolicyAlways
	p.Spec.InitContainers = []corev1.Container{{Name: "sidecar", RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")}}}, {Name: "init", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")}}}}
	p.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m")}
	cpu, _ := Requests(p)
	if cpu != 370 {
		t.Fatalf("expected max(100+50, 300+50)+20 = 370m, got %d", cpu)
	}
}

func TestDefaultTolerationsAndUnknownScheduler(t *testing.T) {
	p := testutil.Pod("p", "source", "100m")
	seconds := int64(300)
	p.Spec.Tolerations = []corev1.Toleration{{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds}}
	nodes := []corev1.Node{*testutil.Node("a", "1", "1Gi")}
	if got := Plan("source", nodes, nil, []corev1.Pod{*p}, nil); got.Result != "Pass" {
		t.Fatal(got)
	}
	p.Spec.SchedulerName = "custom"
	if got := Plan("source", nodes, nil, []corev1.Pod{*p}, nil); got.Result != "Unknown" {
		t.Fatal(got)
	}
}
