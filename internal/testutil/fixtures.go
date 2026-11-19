// Package testutil 提供单测共享的最小 API 对象，不伪造集群验收证据。
package testutil

import (
	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

func Scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = maintenancev1.AddToScheme(s)
	return s
}
func Node(name, cpu, memory string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{corev1.LabelHostname: name}}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory), corev1.ResourcePods: resource.MustParse("110")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}
func Pod(name, node, cpu string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name + "-uid")}, Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "app", Image: "test:v1", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse("32Mi")}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
}
func Owner(kind, name string, uid types.UID) metav1.OwnerReference {
	yes := true
	version := "apps/v1"
	if kind == "Job" {
		version = "batch/v1"
	}
	if kind == "ReplicationController" {
		version = "v1"
	}
	return metav1.OwnerReference{APIVersion: version, Kind: kind, Name: name, UID: uid, Controller: &yes}
}
func Task(name, node, policy string) *maintenancev1.NodeMaintenance {
	return &maintenancev1.NodeMaintenance{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Generation: 1}, Spec: maintenancev1.NodeMaintenanceSpec{NodeName: node, Mode: "Execute", ExecutionPolicy: policy, DesiredState: "Maintenance", TimeoutSeconds: 1800, JobPolicy: "Default", Drain: maintenancev1.DrainOptions{IgnoreDaemonSets: true, GracePeriodSeconds: -1, ChunkSize: 500, DryRun: "Client"}}}
}
