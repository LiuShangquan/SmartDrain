package e2e

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/planner"
	"github.com/smartdrain/smartdrain/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestFragmentedCapacityBeforeEviction(t *testing.T) {
	if os.Getenv("SMARTDRAIN_EXPERIMENTS") != "1" {
		t.Skip("explicit repeated experiment suite required")
	}
	for round := 1; round <= 3; round++ {
		for _, policy := range []string{maintenancev1.PolicyGuarded, maintenancev1.PolicyCompatible, "kubectl"} {
			t.Run(policy+"/run-"+strconv.Itoa(round), func(t *testing.T) {
				e := setup(t)
				e.cordon("smartdrain-dev-worker2", true)
				e.cordon("smartdrain-dev-worker3", true)
				labels := map[string]string{"app": "large"}
				tpl := template(labels, "0")
				tpl.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("600m")
				one := int32(1)
				d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "large", Namespace: e.namespace}, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: tpl}}
				if err := e.c.Create(e.ctx, d); err != nil {
					t.Fatal(err)
				}
				e.until("large-ready", time.Minute, func() bool {
					_ = e.c.Get(e.ctx, client.ObjectKeyFromObject(d), d)
					return d.Status.AvailableReplicas == 1
				})
				e.cordon("smartdrain-dev-worker2", false)
				e.cordon("smartdrain-dev-worker3", false)
				// 真正由 scheduler 绑定占位 Pod，不修改 Node allocatable 或伪造 status。
				for i, nodeName := range []string{"smartdrain-dev-worker2", "smartdrain-dev-worker3"} {
					n := &corev1.Node{}
					_ = e.c.Get(e.ctx, client.ObjectKey{Name: nodeName}, n)
					all := &corev1.PodList{}
					_ = e.c.List(e.ctx, all)
					free := n.Status.Allocatable.Cpu().MilliValue()
					for j := range all.Items {
						p := &all.Items[j]
						if p.Spec.NodeName == nodeName && !workload.Terminal(p) {
							cpu, _ := planner.Requests(p)
							free -= cpu
						}
					}
					if free <= 1000 {
						t.Fatal("fixture needs at least 1 CPU free")
					}
					fillerTemplate := template(map[string]string{"app": "filler"}, "0")
					fillerTemplate.Spec.NodeSelector = map[string]string{corev1.LabelHostname: nodeName}
					fillerTemplate.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(free-400, resource.DecimalSI)
					filler := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "filler-" + strconv.Itoa(i), Namespace: e.namespace}, Spec: fillerTemplate.Spec}
					if err := e.c.Create(e.ctx, filler); err != nil {
						t.Fatal(err)
					}
					e.until("filler-bound", time.Minute, func() bool {
						_ = e.c.Get(e.ctx, client.ObjectKeyFromObject(filler), filler)
						return filler.Spec.NodeName == nodeName && workload.PodReady(filler)
					})
				}
				var task *maintenancev1.NodeMaintenance
				if policy == "kubectl" {
					binary := os.Getenv("SMARTDRAIN_KUBECTL")
					if binary == "" {
						t.Fatal("SMARTDRAIN_KUBECTL must name verified v1.35.0 kubectl")
					}
					output, err := exec.CommandContext(e.ctx, binary, "--kubeconfig", os.Getenv("KUBECONFIG"), "drain", e.source, "--ignore-daemonsets", "--timeout=30s").CombinedOutput()
					t.Logf("native drain: %s", output)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { e.cordon(e.source, false) })
				} else {
					task = e.task("capacity", policy)
					if policy == maintenancev1.PolicyGuarded {
						e.until("capacity-blocked", time.Minute, func() bool { return e.getTask(task).Status.Compute.Result == "Blocked" })
						n := &corev1.Node{}
						_ = e.c.Get(e.ctx, client.ObjectKey{Name: e.source}, n)
						if n.Spec.Unschedulable || e.getTask(task).Status.CompletedOperations != 0 {
							t.Fatal("Guarded changed source before rejecting fragmented capacity")
						}
					} else {
						e.until("capacity-drained", time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "SourceDrained") })
					}
				}
				if policy != maintenancev1.PolicyGuarded {
					e.until("replacement-pending", time.Minute, func() bool {
						pods := &corev1.PodList{}
						_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace), client.MatchingLabels(labels))
						return len(pods.Items) == 1 && pods.Items[0].Status.Phase == corev1.PodPending && pods.Items[0].Spec.NodeName == ""
					})
				}
				e.dump("fragmented-" + policy)
				if task != nil {
					e.restore(task)
				}
			})
		}
	}
}
