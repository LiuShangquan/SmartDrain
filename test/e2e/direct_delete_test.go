package e2e

import (
	"os"
	"os/exec"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestExplicitDirectDeleteRequiresDeploymentOptIn(t *testing.T) {
	if os.Getenv("SMARTDRAIN_DIRECT_DELETE_TESTS") != "1" {
		t.Skip("explicit optional RBAC suite required")
	}
	e := setup(t)
	e.deployment("web", 1, "0")
	min := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "strict", Namespace: e.namespace}, Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &min, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}
	if err := e.c.Create(e.ctx, pdb); err != nil {
		t.Fatal(err)
	}
	e.until("direct-pdb-observed", time.Minute, func() bool {
		_ = e.c.Get(e.ctx, client.ObjectKeyFromObject(pdb), pdb)
		return pdb.Status.CurrentHealthy == 1 && pdb.Status.DisruptionsAllowed == 0
	})
	configure := func(t *maintenancev1.NodeMaintenance) { t.Spec.Drain.DisableEviction = true }
	blocked := e.task("delete-disabled", maintenancev1.PolicyCompatible, configure)
	e.until("direct-disabled", time.Minute, func() bool {
		condition := meta.FindStatusCondition(e.getTask(blocked).Status.Conditions, "Blocked")
		return condition != nil && condition.Reason == "DirectDeleteNotEnabled"
	})
	if e.getTask(blocked).Status.CompletedOperations != 0 {
		t.Fatal("default deployment allowed Delete")
	}
	e.restore(blocked)
	run := func(args ...string) {
		all := append([]string{"--kubeconfig", os.Getenv("KUBECONFIG")}, args...)
		if output, err := exec.CommandContext(e.ctx, "kubectl", all...).CombinedOutput(); err != nil {
			t.Fatalf("kubectl: %v: %s", err, output)
		}
	}
	t.Cleanup(func() {
		run("apply", "-k", "../../config/default")
		run("delete", "-f", "../../config/rbac/direct_delete.yaml", "--ignore-not-found")
		run("-n", "smartdrain-system", "rollout", "status", "deployment/smartdrain-controller-manager", "--timeout=120s")
	})
	run("apply", "-f", "../../config/rbac/direct_delete.yaml")
	run("apply", "-k", "../../config/optional/direct-delete")
	run("-n", "smartdrain-system", "rollout", "status", "deployment/smartdrain-controller-manager", "--timeout=120s")
	task := e.task("delete-enabled", maintenancev1.PolicyCompatible, configure)
	e.until("direct-delete-complete", time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "MaintenanceReady") })
	got := e.getTask(task)
	if got.Status.CompletedOperations != 1 || !meta.IsStatusConditionFalse(got.Status.Conditions, "PDBProtection") {
		t.Fatal("explicit Delete not reported correctly")
	}
	e.dump("explicit-delete")
	e.restore(task)
	manager := &appsv1.Deployment{}
	_ = e.c.Get(e.ctx, client.ObjectKey{Namespace: "smartdrain-system", Name: "smartdrain-controller-manager"}, manager)
	if len(manager.Spec.Template.Spec.Containers[0].Args) != 2 {
		t.Fatal("test did not activate explicit deployment flag")
	}
}
