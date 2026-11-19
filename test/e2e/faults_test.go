package e2e

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/controller"
	"github.com/smartdrain/smartdrain/internal/maintenance"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type lostResponse struct {
	real  maintenance.Executor
	calls int
}

func (e *lostResponse) Submit(ctx context.Context, op *maintenancev1.Operation, opts maintenancev1.DrainOptions, dry bool) error {
	e.calls++
	if err := e.real.Submit(ctx, op, opts, dry); err != nil {
		return err
	}
	return io.EOF // 真正调用 API 成功之后丢弃响应；只存在于测试适配器。
}

func TestAcceptedResponseLostAcrossRestartAndRestore(t *testing.T) {
	if os.Getenv("SMARTDRAIN_FAULT_TESTS") != "1" {
		t.Skip("explicit fault suite required")
	}
	e := setup(t)
	e.deployment("fault", 1, "0")
	pods := &corev1.PodList{}
	_ = e.c.List(e.ctx, pods, client.InNamespace(e.namespace))
	p := pods.Items[0].DeepCopy()
	base := p.DeepCopy()
	p.Finalizers = append(p.Finalizers, "smartdrain.dev/test-hold")
	if err := e.c.Patch(e.ctx, p, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}
	manager := &appsv1.Deployment{}
	key := client.ObjectKey{Namespace: "smartdrain-system", Name: "smartdrain-controller-manager"}
	if err := e.c.Get(e.ctx, key, manager); err != nil {
		t.Fatal(err)
	}
	previous := *manager.Spec.Replicas
	baseManager := manager.DeepCopy()
	zero := int32(0)
	manager.Spec.Replicas = &zero
	if err := e.c.Patch(e.ctx, manager, client.MergeFrom(baseManager)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 失败时也解除自己创建的测试 finalizer，再让真实 manager 收敛任务。
		current := &corev1.Pod{}
		if e.c.Get(e.ctx, client.ObjectKeyFromObject(p), current) == nil && current.UID == p.UID {
			before := current.DeepCopy()
			current.Finalizers = nil
			_ = e.c.Patch(e.ctx, current, client.MergeFrom(before))
		}
		_ = e.c.Get(e.ctx, key, manager)
		before := manager.DeepCopy()
		manager.Spec.Replicas = &previous
		_ = e.c.Patch(e.ctx, manager, client.MergeFrom(before))
	})
	e.until("manager-stopped", time.Minute, func() bool {
		list := &corev1.PodList{}
		_ = e.c.List(e.ctx, list, client.InNamespace("smartdrain-system"), client.MatchingLabels{"app.kubernetes.io/name": "smartdrain"})
		return len(list.Items) == 0
	})
	adminConfig, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := kubernetes.NewForConfig(adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	seconds := int64(600)
	token, err := admin.CoreV1().ServiceAccounts("smartdrain-system").CreateToken(e.ctx, "smartdrain-controller-manager", &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &seconds}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	config := rest.CopyConfig(adminConfig)
	config.CertData = nil
	config.KeyData = nil
	config.CertFile = ""
	config.KeyFile = ""
	config.BearerToken = token.Status.Token
	config.BearerTokenFile = ""
	writer, err := client.New(config, client.Options{Scheme: e.c.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	executor := &lostResponse{real: maintenance.KubernetesExecutor{Client: cs}}
	newReconciler := func() *controller.NodeMaintenanceReconciler {
		return &controller.NodeMaintenanceReconciler{Client: writer, APIReader: writer, Namespace: "smartdrain-system", Executor: executor, EvictionSupported: true, PollInterval: time.Second}
	}
	r := newReconciler()
	task := e.task("lost-response", maintenancev1.PolicyCompatible)
	step := func() {
		if _, err := r.Reconcile(e.ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(task)}); err != nil {
			t.Fatal(err)
		}
	}
	e.until("real-accepted-response-lost", time.Minute, func() bool {
		step()
		got := e.getTask(task)
		return got.Status.CurrentOperation != nil && got.Status.CurrentOperation.UnresolvedUnknown
	})
	currentPod := &corev1.Pod{}
	if err := e.c.Get(e.ctx, client.ObjectKeyFromObject(p), currentPod); err != nil || currentPod.DeletionTimestamp == nil {
		t.Fatalf("server did not really accept eviction: %v", err)
	}
	e.dump("response-lost")
	r = newReconciler() // 丢弃所有内存观察，模拟进程重启。
	got := e.getTask(task)
	before := got.DeepCopy()
	got.Spec.DesiredState = "Restored"
	if err := e.c.Patch(e.ctx, got, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	step()
	got = e.getTask(task)
	if !meta.IsStatusConditionTrue(got.Status.Conditions, "CleanupBlocked") || executor.calls != 1 {
		t.Fatal("unresolved operation was abandoned or resubmitted")
	}
	beforePod := currentPod.DeepCopy()
	currentPod.Finalizers = nil
	if err := e.c.Patch(e.ctx, currentPod, client.MergeFrom(beforePod)); err != nil {
		t.Fatal(err)
	}
	e.until("restart-cleanup-complete", time.Minute, func() bool { step(); return e.getTask(task).Status.Phase == "Completed" })
	if executor.calls != 1 {
		t.Fatal("restore submitted a second operation")
	}
	n := &corev1.Node{}
	_ = e.c.Get(e.ctx, client.ObjectKey{Name: e.source}, n)
	if n.Spec.Unschedulable || n.Annotations[maintenancev1.ClaimAnnotation] != "" {
		t.Fatal("node cleanup incomplete")
	}
	e.dump("restart-restored")
}
