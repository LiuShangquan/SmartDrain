package workload

import (
	"context"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClassifyNativeOptions(t *testing.T) {
	now := time.Now()
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "test", UID: "ds"}}
	c := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithObjects(ds).Build()
	cases := []struct {
		name, want string
		change     func(*corev1.Pod, *maintenancev1.NodeMaintenanceSpec)
	}{
		{"managed", Evict, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {}},
		{"unknown-controller", Evict, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.OwnerReferences[0].Kind = "CustomWorkload"
		}},
		{"bare-block", Block, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) { p.OwnerReferences = nil }},
		{"bare-force", Evict, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.OwnerReferences = nil
			s.Drain.Force = true
		}},
		{"emptydir-block", Block, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.Spec.Volumes = []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
		}},
		{"emptydir-consent", Evict, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.Spec.Volumes = []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
			s.Drain.DeleteEmptyDirData = true
		}},
		{"pvc-compatible", Evict, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}
		}},
		{"daemonset-retain", Retain, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("DaemonSet", "agent", "ds")}
		}},
		{"daemonset-block", Block, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("DaemonSet", "agent", "ds")}
			s.Drain.IgnoreDaemonSets = false
		}},
		{"orphan-ds-force", Evict, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("DaemonSet", "gone", "gone")}
			s.Drain.Force = true
		}},
		{"mirror-retain", Retain, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "unit-fixture"}
		}},
		{"terminal-bare", Evict, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.OwnerReferences = nil
			p.Status.Phase = corev1.PodSucceeded
		}},
		{"job-evict", Evict, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("Job", "job", "job")}
		}},
		{"job-wait-emptydir", Wait, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("Job", "job", "job")}
			s.JobPolicy = "Wait"
			p.Spec.Volumes = []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
		}},
		{"selector", Excluded, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) { s.Drain.PodSelector = "app=other" }},
		{"skip-terminating", Skip, func(p *corev1.Pod, s *maintenancev1.NodeMaintenanceSpec) {
			at := metav1.NewTime(now.Add(-time.Hour))
			p.DeletionTimestamp = &at
			s.Drain.SkipWaitForDeleteTimeoutSeconds = 5
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testutil.Pod("p", "source", "100m")
			p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("ReplicaSet", "rs", "rs")}
			spec := testutil.Task("t", "source", maintenancev1.PolicyCompatible).Spec
			tc.change(p, &spec)
			got := Classify(context.Background(), c, p, spec, now)
			if got.Action != tc.want {
				t.Fatalf("got %s (%s), want %s", got.Action, got.Reason, tc.want)
			}
		})
	}
}

func TestCordonBypassWildcard(t *testing.T) {
	if !BypassesCordon(&corev1.PodSpec{Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}}}) {
		t.Fatal("empty-key Exists must match")
	}
	seconds := int64(300)
	if BypassesCordon(&corev1.PodSpec{Tolerations: []corev1.Toleration{{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds}}}) {
		t.Fatal("default not-ready toleration is not cordon bypass")
	}
}
