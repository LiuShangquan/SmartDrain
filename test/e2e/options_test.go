package e2e

import (
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (e *environment) bare(name string, emptyDir bool) *corev1.Pod {
	tpl := template(map[string]string{"app": name}, "0")
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: e.namespace, Labels: tpl.Labels}, Spec: tpl.Spec}
	p.Spec.NodeName = e.source
	if emptyDir {
		p.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	}
	if err := e.c.Create(e.ctx, p); err != nil {
		e.t.Fatal(err)
	}
	e.until("bare-running", time.Minute, func() bool {
		_ = e.c.Get(e.ctx, client.ObjectKeyFromObject(p), p)
		return p.Status.Phase == corev1.PodRunning
	})
	return p
}

func TestNativeOptionsAndPartialScope(t *testing.T) {
	e := setup(t)
	e.bare("selected", true)
	e.bare("unselected", false)
	blocked := e.task("default", maintenancev1.PolicyCompatible)
	e.until("native-filter-block", time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(blocked).Status.Conditions, "Blocked") })
	if e.getTask(blocked).Status.CompletedOperations != 0 {
		t.Fatal("default filter removed bare Pod")
	}
	e.restore(blocked)
	selected := e.task("selected", maintenancev1.PolicyCompatible, func(t *maintenancev1.NodeMaintenance) {
		t.Spec.Drain.Force = true
		t.Spec.Drain.DeleteEmptyDirData = true
		t.Spec.Drain.PodSelector = "app=selected"
		t.Spec.Drain.GracePeriodSeconds = 0
	})
	e.until("partial-holding", time.Minute, func() bool { return e.getTask(selected).Status.Phase == "Holding" })
	got := e.getTask(selected)
	if got.Status.CompletedOperations != 1 || meta.IsStatusConditionTrue(got.Status.Conditions, "MaintenanceReady") || !meta.IsStatusConditionTrue(got.Status.Conditions, "DrainComplete") {
		t.Fatal("partial scope reported node maintenance ready")
	}
	e.dump("partial-holding")
	e.restore(selected)
	all := e.task("all", maintenancev1.PolicyCompatible, func(t *maintenancev1.NodeMaintenance) { t.Spec.Drain.Force = true; t.Spec.Drain.GracePeriodSeconds = 0 })
	e.until("forced-drain", time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(all).Status.Conditions, "MaintenanceReady") })
	if e.getTask(all).Status.CompletedOperations != 1 {
		t.Fatal("unselected bare Pod lost")
	}
	e.restore(all)
}

func TestSkipWaitKeepsCleanupResponsibility(t *testing.T) {
	e := setup(t)
	p := e.bare("held", false)
	before := p.DeepCopy()
	p.Finalizers = []string{"smartdrain.dev/test-hold"}
	if err := e.c.Patch(e.ctx, p, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		current := &corev1.Pod{}
		if e.c.Get(e.ctx, client.ObjectKeyFromObject(p), current) == nil && current.UID == p.UID {
			b := current.DeepCopy()
			current.Finalizers = nil
			_ = e.c.Patch(e.ctx, current, client.MergeFrom(b))
		}
	})
	task := e.task("skip", maintenancev1.PolicyCompatible, func(t *maintenancev1.NodeMaintenance) {
		t.Spec.Drain.Force = true
		t.Spec.Drain.GracePeriodSeconds = 0
		t.Spec.Drain.SkipWaitForDeleteTimeoutSeconds = 1
	})
	e.until("skip-pending-cleanup", time.Minute, func() bool { return len(e.getTask(task).Status.PendingCleanup) == 1 })
	got := e.getTask(task)
	base := got.DeepCopy()
	got.Spec.DesiredState = "Restored"
	if err := e.c.Patch(e.ctx, got, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}
	e.until("skip-cleanup-blocked", time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "CleanupBlocked") })
	n := &corev1.Node{}
	_ = e.c.Get(e.ctx, client.ObjectKey{Name: e.source}, n)
	if !n.Spec.Unschedulable {
		t.Fatal("uncordoned before skipped request resolved")
	}
	e.dump("skip-cleanup-blocked")
	_ = e.c.Get(e.ctx, client.ObjectKeyFromObject(p), p)
	before = p.DeepCopy()
	p.Finalizers = nil
	if err := e.c.Patch(e.ctx, p, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	e.until("skip-restored", time.Minute, func() bool { return e.getTask(task).Status.Phase == "Completed" })
}
