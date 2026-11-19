package workload

import (
	"context"
	"testing"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReplicaIdentityTemplateAndHealth(t *testing.T) {
	p := testutil.Pod("one", "receiver", "100m")
	p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("ReplicaSet", "rs", "rs-uid")}
	p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "CONFIG", Value: "stable"}}
	template := corev1.PodTemplateSpec{Spec: *p.Spec.DeepCopy()}
	template.Spec.NodeName = ""
	in := Inspection{AvailableReplicas: 1, Baseline: maintenancev1.WorkloadBaseline{ObjectIdentity: maintenancev1.ObjectIdentity{Kind: "ReplicaSet", Namespace: "test", UID: "rs-uid"}, Desired: 1}, Template: template}
	nodes := map[string]*corev1.Node{"receiver": testutil.Node("receiver", "1", "1Gi")}
	ok, fingerprint, why := Healthy(in, []corev1.Pod{*p}, nodes)
	if !ok {
		t.Fatal(why)
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 1}}
	if ok, next, _ := Healthy(in, []corev1.Pod{*p}, nodes); !ok || next == fingerprint {
		t.Fatal("restart did not reset fingerprint")
	}
	p.Spec.Containers = append([]corev1.Container{{Name: "injected", Image: "sidecar:v1"}}, p.Spec.Containers...)
	if ok, _, why := Healthy(in, []corev1.Pod{*p}, nodes); !ok {
		t.Fatal("injection rejected:", why)
	}
	p.Spec.Containers[1].Env[0].Value = "changed"
	if ok, _, _ := Healthy(in, []corev1.Pod{*p}, nodes); ok {
		t.Fatal("declared env drift accepted")
	}
	p.Spec.Containers[1].Env[0].Value = "stable"
	p.OwnerReferences[0].UID = "same-name-new-owner"
	if ok, _, _ := Healthy(in, []corev1.Pod{*p}, nodes); ok {
		t.Fatal("foreign UID counted as recovery")
	}
}

func TestMissingDeploymentNeverBecomesIndependentReplicaSet(t *testing.T) {
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "test", UID: "rs-uid", OwnerReferences: []metav1.OwnerReference{testutil.Owner("Deployment", "missing", "deployment-uid")}}}
	p := testutil.Pod("p", "source", "100m")
	p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("ReplicaSet", "rs", "rs-uid")}
	c := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithObjects(rs).Build()
	if _, err := Inspect(context.Background(), c, p); err == nil {
		t.Fatal("missing parent downgraded to independent RS")
	}
}
