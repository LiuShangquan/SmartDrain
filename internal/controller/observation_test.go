package controller

import (
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/testutil"
	"github.com/smartdrain/smartdrain/internal/workload"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestJobDisappearanceIsNotSuccess(t *testing.T) {
	task := testutil.Task("t", "source", maintenancev1.PolicyGuarded)
	p := testutil.Pod("job-pod", "source", "100m")
	p.OwnerReferences = []metav1.OwnerReference{testutil.Owner("Job", "job", "job-uid")}
	s := snapshot{source: []corev1.Pod{*p}, reports: []maintenancev1.PodReport{{ObjectIdentity: workload.Identity(p), Action: workload.Wait}}}
	if err := observeJobs(task, s); err != nil {
		t.Fatal(err)
	}
	if err := observeJobs(task, snapshot{}); err != nil {
		t.Fatal(err)
	}
	if len(task.Status.JobObservations) != 1 || task.Status.JobObservations[0].Outcome != "GoneWithoutObservedResult" {
		t.Fatal(task.Status.JobObservations)
	}
}

func TestAPIErrorReasonRemainsValidCondition(t *testing.T) {
	for _, message := range []string{"replicasets.apps \"old\" not found", "500 internal error", "dial tcp: connection refused", "unexpected-transport.error"} {
		condition := metav1.Condition{Type: "Blocked", Status: metav1.ConditionTrue, Reason: reasonToken(message), Message: message, LastTransitionTime: metav1.NewTime(time.Now())}
		if errs := metavalidation.ValidateCondition(condition, field.NewPath("status", "conditions")); len(errs) > 0 {
			t.Fatal(errs)
		}
	}
}
