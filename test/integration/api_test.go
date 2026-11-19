package integration

import (
	"context"
	"os"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestCRDContract 使用实际 API Server 验证默认、CEL、status 与乐观并发。
func TestCRDContract(t *testing.T) {
	if os.Getenv("SMARTDRAIN_API_TESTS") != "1" {
		t.Skip("set SMARTDRAIN_API_TESTS=1 with the dedicated test kubeconfig")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.NewScheme()
	_ = maintenancev1.AddToScheme(s)
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := "contract-" + time.Now().Format("150405")
	defaults := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "maintenance.example.com/v1alpha1", "kind": "NodeMaintenance", "metadata": map[string]any{"name": name + "-defaults"}, "spec": map[string]any{"nodeName": "nonexistent-contract-node", "mode": "PlanOnly"}}}
	if err := c.Create(ctx, defaults); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, defaults) })
	policy, _, _ := unstructured.NestedString(defaults.Object, "spec", "executionPolicy")
	timeout, _, _ := unstructured.NestedInt64(defaults.Object, "spec", "timeoutSeconds")
	grace, _, _ := unstructured.NestedInt64(defaults.Object, "spec", "drain", "gracePeriodSeconds")
	chunkSize, _, _ := unstructured.NestedInt64(defaults.Object, "spec", "drain", "chunkSize")
	if policy != "Guarded" || timeout != 1800 || grace != -1 || chunkSize != 500 {
		t.Fatalf("unexpected server defaults: %+v", defaults.Object["spec"])
	}
	obj := &maintenancev1.NodeMaintenance{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: maintenancev1.NodeMaintenanceSpec{NodeName: "nonexistent-contract-node", Mode: "PlanOnly", ExecutionPolicy: maintenancev1.PolicyCompatible, DesiredState: "Maintenance", TimeoutSeconds: 0, JobPolicy: "Default", Drain: maintenancev1.DrainOptions{GracePeriodSeconds: 0, ChunkSize: 0, DryRun: "Client"}}}
	if err := c.Create(ctx, obj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, obj) })
	if obj.Spec.TimeoutSeconds != 0 || obj.Spec.Drain.GracePeriodSeconds != 0 || obj.Spec.Drain.ChunkSize != 0 {
		t.Fatalf("explicit zero was replaced by defaults: %+v", obj.Spec)
	}
	current := &maintenancev1.NodeMaintenance{}
	key := client.ObjectKey{Name: name}
	mutate := func(change func(*maintenancev1.NodeMaintenance)) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh := &maintenancev1.NodeMaintenance{}
			if err := c.Get(ctx, key, fresh); err != nil {
				return err
			}
			change(fresh)
			return c.Update(ctx, fresh)
		})
	}
	if err := c.Get(ctx, key, current); err != nil {
		t.Fatal(err)
	}
	if err := mutate(func(t *maintenancev1.NodeMaintenance) { t.Spec.NodeName = "another-node" }); !apierrors.IsInvalid(err) {
		t.Fatalf("immutable nodeName accepted: %v", err)
	}
	if err := mutate(func(t *maintenancev1.NodeMaintenance) { t.Spec.DesiredState = "Restored" }); err != nil {
		t.Fatal(err)
	}
	if err := mutate(func(t *maintenancev1.NodeMaintenance) { t.Spec.DesiredState = "Maintenance" }); !apierrors.IsInvalid(err) {
		t.Fatalf("reverse intent accepted: %v", err)
	}
	if err := c.Get(ctx, key, current); err != nil {
		t.Fatal(err)
	}
	var stale *maintenancev1.NodeMaintenance
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, key, current); err != nil {
			return err
		}
		stale = current.DeepCopy()
		current.Status.Phase = "ContractProbe"
		return c.Status().Update(ctx, current)
	}); err != nil {
		t.Fatal(err)
	}
	stale.Status.Phase = "Pending"
	if err := c.Status().Update(ctx, stale); !apierrors.IsConflict(err) {
		t.Fatalf("stale status update accepted: %v", err)
	}
	invalid := obj.DeepCopy()
	invalid.ResourceVersion = ""
	invalid.UID = ""
	invalid.Name = name + "-invalid"
	invalid.Spec.ExecutionPolicy = maintenancev1.PolicyGuarded
	invalid.Spec.Drain.DisableEviction = true
	if err := c.Create(ctx, invalid); !apierrors.IsInvalid(err) {
		t.Fatalf("Guarded bypass accepted: %v", err)
	}
}
