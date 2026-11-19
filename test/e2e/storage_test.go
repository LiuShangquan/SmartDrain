package e2e

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestNFSCrossNodeDataReadback(t *testing.T) {
	server := os.Getenv("SMARTDRAIN_NFS_SERVER")
	if server == "" {
		t.Skip("set SMARTDRAIN_NFS_SERVER after installing the real NFS fixture")
	}
	for _, kind := range []string{"Deployment", "StatefulSet"} {
		t.Run(kind, func(t *testing.T) {
			e := setup(t)
			class := ""
			pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: e.namespace}, Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, MountOptions: []string{"nfsvers=4.1"}, PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nfs.csi.k8s.io", VolumeHandle: e.namespace, VolumeAttributes: map[string]string{"server": server, "share": "/"}}}}}
			if err := e.c.Create(e.ctx, pv); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.c.Delete(e.ctx, pv) })
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: e.namespace}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class, VolumeName: pv.Name, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
			if err := e.c.Create(e.ctx, pvc); err != nil {
				t.Fatal(err)
			}
			e.cordon("smartdrain-dev-worker2", true)
			e.cordon("smartdrain-dev-worker3", true)
			labels := map[string]string{"app": "storage"}
			tpl := template(labels, "1")
			group := int64(1000)
			tpl.Spec.SecurityContext = &corev1.PodSecurityContext{FSGroup: &group}
			tpl.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}
			tpl.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}
			tpl.Spec.Containers[0].Env = append(tpl.Spec.Containers[0].Env, corev1.EnvVar{Name: "DATA_DIR", Value: "/data/" + e.namespace})
			one := int32(1)
			metadata := metav1.ObjectMeta{Name: "storage", Namespace: e.namespace}
			selector := &metav1.LabelSelector{MatchLabels: labels}
			var obj client.Object
			if kind == "Deployment" {
				obj = &appsv1.Deployment{ObjectMeta: metadata, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: selector, Template: tpl}}
			} else {
				obj = &appsv1.StatefulSet{ObjectMeta: metadata, Spec: appsv1.StatefulSetSpec{Replicas: &one, Selector: selector, Template: tpl, ServiceName: "storage"}}
				max := intstr.FromInt32(1)
				if err := e.c.Create(e.ctx, &policyv1.PodDisruptionBudget{ObjectMeta: metadata, Spec: policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &max, Selector: selector}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.c.Create(e.ctx, obj); err != nil {
				t.Fatal(err)
			}
			var original corev1.Pod
			e.until("nfs-source-ready", 2*time.Minute, func() bool {
				pods := &corev1.PodList{}
				_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
				if len(pods.Items) == 1 && workload.PodReady(&pods.Items[0]) {
					original = pods.Items[0]
					return true
				}
				return false
			})
			if original.Spec.NodeName != e.source {
				t.Fatal("NFS fixture on wrong source")
			}
			cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
			if err != nil {
				t.Fatal(err)
			}
			cs, err := kubernetes.NewForConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			readToken := func(name string) string {
				data, err := cs.CoreV1().RESTClient().Get().Namespace(e.namespace).Resource("pods").Name(name + ":8080").SubResource("proxy").DoRaw(e.ctx)
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]string
				if err := json.Unmarshal(data, &result); err != nil {
					t.Fatal(err)
				}
				return result["token"]
			}
			before := readToken(original.Name)
			if before == "" {
				t.Fatal("missing persisted token")
			}
			e.dump("nfs-before")
			e.cordon("smartdrain-dev-worker2", false)
			e.cordon("smartdrain-dev-worker3", false)
			task := e.task("nfs", maintenancev1.PolicyGuarded)
			e.until("nfs-maintenance", 3*time.Minute, func() bool { return meta.IsStatusConditionTrue(e.getTask(task).Status.Conditions, "MaintenanceReady") })
			pods := &corev1.PodList{}
			_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
			if len(pods.Items) != 1 || pods.Items[0].UID == original.UID || pods.Items[0].Spec.NodeName == e.source {
				t.Fatal("replacement did not move across nodes")
			}
			after := readToken(pods.Items[0].Name)
			if after != before {
				t.Fatalf("persistent token changed: %s -> %s", before, after)
			}
			t.Logf("%s: Pod UID %s -> %s; Node %s -> %s; data token %s", kind, original.UID, pods.Items[0].UID, e.source, pods.Items[0].Spec.NodeName, after)
			e.dump("nfs-readback")
			e.restore(task)
		})
	}
}
