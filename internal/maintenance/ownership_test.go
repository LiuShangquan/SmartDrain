package maintenance

import (
	"context"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/testutil"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLeaseAndCordonCleanupReceipts(t *testing.T) {
	ctx := context.Background()
	n := testutil.Node("source", "1", "1Gi")
	c := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithObjects(n).Build()
	o := Ownership{Reader: c, Writer: c, Namespace: "system"}
	task := testutil.Task("one", "source", maintenancev1.PolicyGuarded)
	task.Status.NodeUID = n.UID
	task.Status.NodeClaimStarted = true
	ok, start, err := o.Acquire(ctx, task, time.Now())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	ok, again, err := o.Acquire(ctx, task, time.Now().Add(time.Hour))
	if err != nil || !ok || !again.Equal(start) {
		t.Fatal("acquireTime changed", start, again, err)
	}
	other := testutil.Task("two", "source", maintenancev1.PolicyGuarded)
	ok, _, err = o.Acquire(ctx, other, time.Now())
	if err != nil || ok {
		t.Fatal("other task obtained occupied slot", err)
	}
	if err := o.Cordon(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := o.Cordon(ctx, task); err != nil {
		t.Fatal(err)
	}
	got := &corev1.Node{}
	_ = c.Get(ctx, client.ObjectKey{Name: "source"}, got)
	claim, _ := ReadClaim(got)
	if !got.Spec.Unschedulable || claim.OriginalUnschedulable {
		t.Fatal("original state was recaptured")
	}
	result, err := o.ApplyCleanup(ctx, task)
	if err != nil || result != "Restored" {
		t.Fatal(result, err)
	}
	result, err = o.ApplyCleanup(ctx, task)
	if err != nil || result != "Restored" {
		t.Fatal("receipt replay", result, err)
	}
	task.Status.NodeCleanupConfirmed = true
	task.Status.CleanupResult = result
	if err := o.RemoveClaim(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyCleanup(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := o.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	ok, _, err = o.Acquire(ctx, other, time.Now())
	if err != nil || !ok {
		t.Fatal("slot not released", err)
	}
}

func TestNodeReplacementDoesNotRestoreNewNode(t *testing.T) {
	ctx := context.Background()
	old := testutil.Node("source", "1", "1Gi")
	c := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithObjects(old).Build()
	o := Ownership{Reader: c, Writer: c, Namespace: "system"}
	task := testutil.Task("old-task", "source", maintenancev1.PolicyGuarded)
	task.Status.NodeUID = old.UID
	task.Status.NodeClaimStarted = true
	if err := o.Cordon(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	replacement := testutil.Node("source", "1", "1Gi")
	replacement.UID = "new-node-uid"
	replacement.Spec.Unschedulable = true
	if err := c.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	result, err := o.ApplyCleanup(ctx, task)
	if err != nil || result != "NodeReplaced" {
		t.Fatal(result, err)
	}
	current := &corev1.Node{}
	_ = c.Get(ctx, client.ObjectKey{Name: "source"}, current)
	if !current.Spec.Unschedulable {
		t.Fatal("replacement node modified")
	}
}

func TestCleanupPreservesHoldAndOriginalCordon(t *testing.T) {
	for _, original := range []bool{true, false} {
		t.Run(map[bool]string{true: "original", false: "hold"}[original], func(t *testing.T) {
			ctx := context.Background()
			n := testutil.Node("source", "1", "1Gi")
			n.Spec.Unschedulable = original
			if !original {
				n.Annotations = map[string]string{maintenancev1.HoldAnnotation: "true"}
			}
			c := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithObjects(n).Build()
			o := Ownership{Reader: c, Writer: c}
			task := testutil.Task("t", "source", maintenancev1.PolicyGuarded)
			task.Status.NodeUID = n.UID
			task.Status.NodeClaimStarted = true
			if err := o.Cordon(ctx, task); err != nil {
				t.Fatal(err)
			}
			if _, err := o.ApplyCleanup(ctx, task); err != nil {
				t.Fatal(err)
			}
			got := &corev1.Node{}
			_ = c.Get(ctx, client.ObjectKey{Name: n.Name}, got)
			if !got.Spec.Unschedulable {
				t.Fatal("admin cordon lost")
			}
		})
	}
}
