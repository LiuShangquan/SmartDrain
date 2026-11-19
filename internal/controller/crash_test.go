package controller

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/testutil"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// 每个写入位置都在“已经落盘但响应丢失”后重启，包括 Node 清理与释放槽位。
func TestCrashAfterEveryPersistentWrite(t *testing.T) {
	for crashAt := 1; crashAt <= 14; crashAt++ {
		t.Run(fmt.Sprintf("write-%d", crashAt), func(t *testing.T) {
			ctx := context.Background()
			task := testutil.Task("crash", "source", maintenancev1.PolicyCompatible)
			node := testutil.Node("source", "2", "2Gi")
			node.Labels["unrelated"] = "keep"
			backing := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithStatusSubresource(&maintenancev1.NodeMaintenance{}).WithObjects(task, node, testutil.Node("receiver", "2", "2Gi")).Build()
			writes := 0
			fired := false
			restarted := false
			result := func(err error) error {
				if err != nil {
					return err
				}
				writes++
				if writes == crashAt {
					fired = true
					return io.EOF
				}
				return nil
			}
			broken := interceptor.NewClient(backing, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					return result(c.Create(ctx, obj, opts...))
				},
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					return result(c.Patch(ctx, obj, patch, opts...))
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					return result(c.SubResource(sub).Update(ctx, obj, opts...))
				},
			})
			r := &NodeMaintenanceReconciler{Client: broken, APIReader: backing, Namespace: "system", Executor: &recordingExecutor{client: backing}, EvictionSupported: true, StabilityWindow: time.Nanosecond}
			intentChanged := false
			for i := 0; i < 40; i++ {
				_, _ = r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "crash"}})
				if fired && !restarted {
					r = &NodeMaintenanceReconciler{Client: backing, APIReader: backing, Namespace: "system", Executor: &recordingExecutor{client: backing}, EvictionSupported: true, StabilityWindow: time.Nanosecond}
					restarted = true
				}
				got := getTask(t, backing, "crash")
				if got.Status.Phase == "Maintenance" && !intentChanged {
					got.Spec.DesiredState = "Restored"
					if err := backing.Update(ctx, got); err != nil {
						t.Fatal(err)
					}
					intentChanged = true
				}
				if got.Status.Phase == "Completed" && len(got.Finalizers) == 0 {
					break
				}
			}
			got := getTask(t, backing, "crash")
			if !fired {
				t.Fatalf("crash position not reached: %d writes", writes)
			}
			current := &corev1.Node{}
			_ = backing.Get(ctx, client.ObjectKey{Name: "source"}, current)
			if got.Status.Phase != "Completed" || len(got.Finalizers) > 0 || current.Spec.Unschedulable || current.Labels["unrelated"] != "keep" || current.Annotations[maintenancev1.ClaimAnnotation] != "" {
				t.Fatalf("did not recover: writes=%d fired=%t status=%+v", writes, fired, got.Status)
			}
		})
	}
}

func TestStableWindowResetsOnChangeAndRestart(t *testing.T) {
	now := time.Now()
	r := &NodeMaintenanceReconciler{Now: func() time.Time { return now }}
	if r.isStable("owner", "uid-a/restarts0", 0) {
		t.Fatal("first observation was stable")
	}
	now = now.Add(11 * time.Second)
	if !r.isStable("owner", "uid-a/restarts0", 0) {
		t.Fatal("window did not pass")
	}
	if r.isStable("owner", "uid-a/restarts1", 0) {
		t.Fatal("restart count did not reset")
	}
	r = &NodeMaintenanceReconciler{Now: func() time.Time { return now }}
	if r.isStable("owner", "uid-a/restarts1", 0) {
		t.Fatal("process restart reused old health window")
	}
}
