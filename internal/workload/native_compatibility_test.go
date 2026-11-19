package workload

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubectl/pkg/drain"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNativeFilterParity 直接运行固定 tag 的 kubectl 过滤器，避免两份手写期望一起出错。
func TestNativeFilterParity(t *testing.T) {
	for _, kind := range []string{"unmanaged", "ReplicaSet", "ReplicationController", "StatefulSet", "Job", "Custom", "DaemonSet", "orphanDaemonSet", "mirror"} {
		for _, terminal := range []bool{false, true} {
			for mask := 0; mask < 8; mask++ {
				t.Run(fmt.Sprintf("%s/terminal=%t/options=%d", kind, terminal, mask), func(t *testing.T) {
					p := testutil.Pod("pod", "source", "100m")
					p.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
					if terminal {
						p.Status.Phase = corev1.PodSucceeded
					}
					objects := []client.Object{}
					apiObjects := []runtime.Object{}
					switch kind {
					case "unmanaged":
					case "mirror":
						p.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "real-mirror-tested-separately"}
					case "DaemonSet", "orphanDaemonSet":
						p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("DaemonSet", "ds", "old-uid")}
						if kind == "DaemonSet" {
							ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "ds", Namespace: "test", UID: "new-uid"}}
							objects = append(objects, ds)
							apiObjects = append(apiObjects, ds.DeepCopy())
						}
					default:
						p.OwnerReferences = []metav1.OwnerReference{testutil.Owner(kind, "owner", "owner-uid")}
					}
					spec := testutil.Task("t", "source", maintenancev1.PolicyCompatible).Spec
					spec.Drain.Force = mask&1 != 0
					spec.Drain.IgnoreDaemonSets = mask&2 != 0
					spec.Drain.DeleteEmptyDirData = mask&4 != 0
					reader := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithObjects(objects...).Build()
					apiObjects = append(apiObjects, p.DeepCopy())
					h := drain.Helper{Ctx: context.Background(), Client: kubernetesfake.NewSimpleClientset(apiObjects...), Force: spec.Drain.Force, IgnoreAllDaemonSets: spec.Drain.IgnoreDaemonSets, DeleteEmptyDirData: spec.Drain.DeleteEmptyDirData, Out: io.Discard, ErrOut: io.Discard}
					list, errs := h.GetPodsForDeletion("source")
					expected := Retain
					if len(errs) > 0 {
						expected = Block
					} else if len(list.Pods()) > 0 {
						expected = Evict
					}
					got := Classify(context.Background(), reader, p, spec, time.Now())
					if got.Action != expected {
						t.Fatalf("kubectl=%s SmartDrain=%+v errors=%v", expected, got, errs)
					}
				})
			}
		}
	}
}
