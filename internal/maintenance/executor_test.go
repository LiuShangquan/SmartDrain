package maintenance

import (
	"context"
	"errors"
	"testing"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestUnknownIsSticky(t *testing.T) {
	op := &maintenancev1.Operation{State: "AttemptStarted"}
	RecordResult(op, errors.New("EOF"))
	RecordResult(op, apierrors.NewTooManyRequests("budget", 1))
	if !op.UnresolvedUnknown || !MayHaveExecuted(op) {
		t.Fatal("429 erased historical unknown")
	}
	RecordResult(op, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p", errors.New("denied")))
	if !op.UnresolvedUnknown {
		t.Fatal("403 erased historical unknown")
	}
	op = &maintenancev1.Operation{State: "Prepared"}
	RecordResult(op, apierrors.NewTooManyRequests("budget", 1))
	if MayHaveExecuted(op) {
		t.Fatal("known refusal without historical attempt must remain abandonable")
	}
}

func TestEvictionUIDAndDryRun(t *testing.T) {
	cs := fake.NewSimpleClientset()
	calls := 0
	cs.PrependReactor("create", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		ev := a.(clienttesting.CreateAction).GetObject().(*policyv1.Eviction)
		if ev.DeleteOptions.Preconditions == nil || *ev.DeleteOptions.Preconditions.UID != "old-uid" || len(ev.DeleteOptions.DryRun) != 1 {
			t.Fatalf("unsafe eviction: %+v", ev)
		}
		return true, ev, nil
	})
	e := KubernetesExecutor{Client: cs}
	op := &maintenancev1.Operation{ObjectIdentity: maintenancev1.ObjectIdentity{Namespace: "ns", Name: "p", UID: "old-uid"}, Action: "Evict"}
	if err := e.Submit(context.Background(), op, maintenancev1.DrainOptions{GracePeriodSeconds: -1}, true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
