package workload

import (
	"context"
	"testing"

	"github.com/smartdrain/smartdrain/internal/testutil"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStorageIdentityAndCandidateIntersection(t *testing.T) {
	ctx := context.Background()
	nodes := []corev1.Node{*testutil.Node("a", "1", "1Gi"), *testutil.Node("b", "1", "1Gi")}
	p := testutil.Pod("pod", "source", "100m")
	p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}, {Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "config"}}}}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "test", UID: "claim-uid"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv", Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv", UID: "pv-uid"}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Name: "data", Namespace: "test", UID: pvc.UID}, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nfs.csi.k8s.io", VolumeHandle: "original"}}, NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}}}}}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	no := false
	driver := &storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "nfs.csi.k8s.io"}, Spec: storagev1.CSIDriverSpec{AttachRequired: &no}}
	objects := []client.Object{pvc, pv, driver}
	for _, n := range nodes {
		objects = append(objects, &storagev1.CSINode{ObjectMeta: metav1.ObjectMeta{Name: n.Name}, Spec: storagev1.CSINodeSpec{Drivers: []storagev1.CSINodeDriver{{Name: driver.Name, NodeID: n.Name}}}})
	}
	c := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithObjects(objects...).Build()
	got, err := AnalyzeStorage(ctx, c, p, nodes)
	if err != nil || got.Report.Result != "Pass" || !got.Allowed["a"] || got.Allowed["b"] || len(got.Volumes) != 1 {
		t.Fatalf("bad storage analysis: %+v %v", got, err)
	}
	if err := VerifyVolumes(ctx, c, got.Volumes); err != nil {
		t.Fatal(err)
	}
	current := &corev1.PersistentVolume{}
	_ = c.Get(ctx, client.ObjectKey{Name: pv.Name}, current)
	current.Spec.CSI.VolumeHandle = "changed"
	if err := c.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := VerifyVolumes(ctx, c, got.Volumes); err == nil {
		t.Fatal("backend identity change accepted")
	}
}
