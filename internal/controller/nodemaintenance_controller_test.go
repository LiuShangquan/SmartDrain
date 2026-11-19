package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/testutil"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type recordingExecutor struct {
	client  client.Client
	calls   int
	err     error
	dryRuns int
}

func (e *recordingExecutor) Submit(ctx context.Context, op *maintenancev1.Operation, _ maintenancev1.DrainOptions, dry bool) error {
	if dry {
		e.dryRuns++
		return e.err
	}
	e.calls++
	if e.err != nil {
		return e.err
	}
	p := &corev1.Pod{}
	if err := e.client.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: op.Name}, p); err != nil {
		return err
	}
	if p.UID != op.UID {
		return errors.New("wrong UID")
	}
	return e.client.Delete(ctx, p)
}

func harness(t *testing.T, task *maintenancev1.NodeMaintenance, objects ...client.Object) (*NodeMaintenanceReconciler, client.Client, *recordingExecutor) {
	t.Helper()
	all := []client.Object{task, testutil.Node("source", "2", "2Gi"), testutil.Node("receiver", "2", "2Gi")}
	all = append(all, objects...)
	c := fake.NewClientBuilder().WithScheme(testutil.Scheme()).WithStatusSubresource(&maintenancev1.NodeMaintenance{}).WithObjects(all...).Build()
	e := &recordingExecutor{client: c}
	r := &NodeMaintenanceReconciler{Client: c, APIReader: c, Executor: e, Namespace: "system", EvictionSupported: true, PollInterval: time.Millisecond, StabilityWindow: time.Millisecond}
	return r, c, e
}
func step(t *testing.T, r *NodeMaintenanceReconciler, name string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: name}}); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
}
func getTask(t *testing.T, c client.Client, name string) *maintenancev1.NodeMaintenance {
	t.Helper()
	obj := &maintenancev1.NodeMaintenance{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestCompatibleLifecycleAndRestore(t *testing.T) {
	task := testutil.Task("t", "source", maintenancev1.PolicyCompatible)
	p := testutil.Pod("p", "source", "100m")
	p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("CustomController", "custom", "owner")}
	r, c, e := harness(t, task, p)
	step(t, r, "t", 12)
	got := getTask(t, c, "t")
	if e.calls != 1 || got.Status.Phase != "Maintenance" || !meta.IsStatusConditionTrue(got.Status.Conditions, "MaintenanceReady") {
		t.Fatalf("calls=%d status=%+v", e.calls, got.Status)
	}
	if meta.IsStatusConditionTrue(got.Status.Conditions, "WorkloadRecovered") {
		t.Fatal("unknown controller recovery was fabricated")
	}
	got.Spec.DesiredState = "Restored"
	if err := c.Update(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	step(t, r, "t", 6)
	got = getTask(t, c, "t")
	n := &corev1.Node{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "source"}, n)
	if got.Status.Phase != "Completed" || n.Spec.Unschedulable || len(got.Finalizers) != 0 {
		t.Fatalf("cleanup failed: %+v node=%+v", got.Status, n.Spec)
	}
}

func TestPlanOnlyHasNoExternalMutation(t *testing.T) {
	task := testutil.Task("plan", "source", maintenancev1.PolicyCompatible)
	task.Spec.Mode = "PlanOnly"
	task.Spec.Drain.DryRun = "Server"
	task.Spec.Drain.Force = true
	r, c, e := harness(t, task, testutil.Pod("p", "source", "100m"))
	step(t, r, "plan", 1)
	n := &corev1.Node{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "source"}, n)
	leases := &coordinationv1.LeaseList{}
	_ = c.List(context.Background(), leases)
	if n.Spec.Unschedulable || len(leases.Items) > 0 || e.calls > 0 || e.dryRuns != 1 {
		t.Fatal("PlanOnly mutated external objects")
	}
}

func TestHistoricalUnknownPauseAndExit(t *testing.T) {
	task := testutil.Task("t", "source", maintenancev1.PolicyCompatible)
	task.Spec.Drain.Force = true
	p := testutil.Pod("p", "source", "100m")
	r, c, e := harness(t, task, p)
	e.err = errors.New("response lost")
	step(t, r, "t", 6)
	got := getTask(t, c, "t")
	if got.Status.CurrentOperation == nil || !got.Status.CurrentOperation.UnresolvedUnknown {
		t.Fatalf("expected persisted unknown: %+v", got.Status)
	}
	got.Spec.Paused = true
	_ = c.Update(context.Background(), got)
	calls := e.calls
	step(t, r, "t", 3)
	if e.calls != calls {
		t.Fatal("pause allowed retry to start deletion")
	}
	got = getTask(t, c, "t")
	got.Spec.DesiredState = "Restored"
	_ = c.Update(context.Background(), got)
	step(t, r, "t", 2)
	got = getTask(t, c, "t")
	if got.Status.Phase != "Restoring" || !meta.IsStatusConditionTrue(got.Status.Conditions, "CleanupBlocked") {
		t.Fatal("unknown operation abandoned")
	}
	if e.calls != calls {
		t.Fatal("restore submitted deletion")
	}
	_ = c.Delete(context.Background(), p)
	step(t, r, "t", 6)
	if getTask(t, c, "t").Status.Phase != "Completed" {
		t.Fatal("did not converge after original UID disappeared")
	}
}

func TestJobOnlyWaitAllowsCordonAndExit(t *testing.T) {
	task := testutil.Task("t", "source", maintenancev1.PolicyGuarded)
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "batch", Namespace: "test", UID: "job"}}
	p := testutil.Pod("p", "source", "100m")
	p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("Job", "batch", "job")}
	r, c, e := harness(t, task, p, job)
	step(t, r, "t", 7)
	n := &corev1.Node{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "source"}, n)
	if !n.Spec.Unschedulable || e.calls != 0 {
		t.Fatal("wait-only task must cordon without eviction")
	}
	got := getTask(t, c, "t")
	if got.Status.Compute.Result != "NotApplicable" {
		t.Fatal(got.Status.Compute)
	}
	got.Spec.DesiredState = "Restored"
	_ = c.Update(context.Background(), got)
	step(t, r, "t", 5)
	if getTask(t, c, "t").Status.Phase != "Completed" {
		t.Fatal("natural Job waiting blocked exit")
	}
}

func TestSecondTaskCannotChangeNode(t *testing.T) {
	task := testutil.Task("one", "source", maintenancev1.PolicyCompatible)
	task.Spec.Drain.Force = true
	r, c, e := harness(t, task, testutil.Pod("p", "source", "100m"))
	e.err = errors.New("unknown")
	step(t, r, "one", 6)
	second := testutil.Task("two", "receiver", maintenancev1.PolicyCompatible)
	_ = c.Create(context.Background(), second)
	step(t, r, "two", 5)
	n := &corev1.Node{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "receiver"}, n)
	if n.Spec.Unschedulable {
		t.Fatal("second task cordoned while slot busy")
	}
}

func TestPartialDrainNeverClaimsNodeReady(t *testing.T) {
	task := testutil.Task("t", "source", maintenancev1.PolicyCompatible)
	task.Spec.Drain.PodSelector = "app=selected"
	task.Spec.Drain.Force = true
	r, c, e := harness(t, task, testutil.Pod("unselected", "source", "100m"))
	step(t, r, "t", 8)
	got := getTask(t, c, "t")
	if got.Status.Phase != "Holding" || e.calls > 0 || meta.IsStatusConditionTrue(got.Status.Conditions, "MaintenanceReady") {
		t.Fatal("selected scope confused with whole node")
	}
}

func TestLatePodInvalidatesPreviousDrainConditions(t *testing.T) {
	task := testutil.Task("late", "source", maintenancev1.PolicyCompatible)
	task.Spec.Drain.Force = true
	r, c, e := harness(t, task)
	step(t, r, "late", 8)
	if !meta.IsStatusConditionTrue(getTask(t, c, "late").Status.Conditions, "SourceDrained") {
		t.Fatal("initial empty node did not drain")
	}
	if err := c.Create(context.Background(), testutil.Pod("new", "source", "100m")); err != nil {
		t.Fatal(err)
	}
	step(t, r, "late", 1)
	got := getTask(t, c, "late")
	if !meta.IsStatusConditionFalse(got.Status.Conditions, "SourceDrained") || !meta.IsStatusConditionFalse(got.Status.Conditions, "DrainComplete") || !meta.IsStatusConditionFalse(got.Status.Conditions, "MaintenanceReady") || e.calls != 0 {
		t.Fatal("stale whole-node success survived a new Pod")
	}
}

func TestPreparedPauseAndDeadlineNeverSubmit(t *testing.T) {
	for _, reason := range []string{"pause", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			task := testutil.Task("prepared", "source", maintenancev1.PolicyCompatible)
			task.Spec.Drain.Force = true
			r, c, e := harness(t, task, testutil.Pod("old", "source", "100m"))
			step(t, r, "prepared", 5)
			got := getTask(t, c, "prepared")
			if got.Status.CurrentOperation == nil || got.Status.CurrentOperation.State != "Prepared" {
				t.Fatal("fixture did not reach Prepared")
			}
			if reason == "pause" {
				got.Spec.Paused = true
				_ = c.Update(context.Background(), got)
			} else {
				past := metav1.NewTime(time.Now().Add(-time.Minute))
				got.Status.Deadline = &past
				_ = c.Status().Update(context.Background(), got)
			}
			step(t, r, "prepared", 3)
			if e.calls != 0 {
				t.Fatal("inhibited Prepared operation submitted")
			}
			got = getTask(t, c, "prepared")
			got.Spec.DesiredState = "Restored"
			_ = c.Update(context.Background(), got)
			step(t, r, "prepared", 6)
			if getTask(t, c, "prepared").Status.Phase != "Completed" || e.calls != 0 {
				t.Fatal("never-started operation blocked exit")
			}
		})
	}
}

func TestRestartedAttemptDoesNotTouchSameNameReplacement(t *testing.T) {
	task := testutil.Task("attempt", "source", maintenancev1.PolicyCompatible)
	task.Spec.Drain.Force = true
	p := testutil.Pod("same-name", "source", "100m")
	r, c, e := harness(t, task, p)
	step(t, r, "attempt", 5)
	got := getTask(t, c, "attempt")
	got.Status.CurrentOperation.State = "AttemptStarted"
	_ = c.Status().Update(context.Background(), got)
	got = getTask(t, c, "attempt")
	got.Spec.Paused = true
	_ = c.Update(context.Background(), got)
	step(t, r, "attempt", 2)
	if !getTask(t, c, "attempt").Status.CurrentOperation.UnresolvedUnknown || e.calls != 0 {
		t.Fatal("restarted request lost uncertainty")
	}
	_ = c.Delete(context.Background(), p)
	replacement := testutil.Pod("same-name", "source", "100m")
	replacement.UID = "new-uid"
	if err := c.Create(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	got = getTask(t, c, "attempt")
	got.Spec.DesiredState = "Restored"
	_ = c.Update(context.Background(), got)
	step(t, r, "attempt", 6)
	current := &corev1.Pod{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(replacement), current); err != nil || current.UID != "new-uid" || e.calls != 0 {
		t.Fatal("same-name new UID was touched", err)
	}
}
