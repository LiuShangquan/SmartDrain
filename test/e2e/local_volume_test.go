package e2e

import (
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestLocalPVCCompatibleDrainReportsRecoveryUnknown(t *testing.T) {
	e := setup(t)
	e.cordon("smartdrain-dev-worker2", true)
	e.cordon("smartdrain-dev-worker3", true)
	class := "standard"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "local-data", Namespace: e.namespace}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	if err := e.c.Create(e.ctx, pvc); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app": "local"}
	tpl := template(labels, "0")
	tpl.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name}}}}
	tpl.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "local", Namespace: e.namespace}, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: tpl}}
	if err := e.c.Create(e.ctx, d); err != nil {
		t.Fatal(err)
	}
	var original corev1.Pod
	e.until("local-pvc-ready", 2*time.Minute, func() bool {
		list := &corev1.PodList{}
		_ = e.c.List(e.ctx, list, client.InNamespace(e.namespace))
		if len(list.Items) == 1 && workload.PodReady(&list.Items[0]) {
			original = list.Items[0]
			return true
		}
		return false
	})
	e.cordon("smartdrain-dev-worker2", false)
	e.cordon("smartdrain-dev-worker3", false)
	guarded := e.task("local-guarded", maintenancev1.PolicyGuarded)
	e.until("local-storage-unknown", time.Minute, func() bool { return e.getTask(guarded).Status.Storage.Result == "Unknown" })
	n := &corev1.Node{}
	_ = e.c.Get(e.ctx, client.ObjectKey{Name: e.source}, n)
	if n.Spec.Unschedulable {
		t.Fatal("Guarded cordoned before storage gate")
	}
	e.restore(guarded)
	task := e.task("local-compatible", maintenancev1.PolicyCompatible)
	e.until("local-source-drained", time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "SourceDrained") })
	list := &corev1.PodList{}
	_ = e.c.List(e.ctx, list, client.InNamespace(e.namespace))
	if len(list.Items) != 1 || list.Items[0].UID == original.UID || list.Items[0].Spec.NodeName != "" || list.Items[0].Status.Phase != corev1.PodPending {
		t.Fatal("expected real Pending replacement for node-bound PVC")
	}
	if meta.FindStatusCondition(e.getTask(task).Status.Conditions, "WorkloadRecovered").Status != metav1.ConditionUnknown {
		t.Fatal("storage recovery fabricated")
	}
	e.dump("local-pvc-pending")
	e.restore(task)
}
