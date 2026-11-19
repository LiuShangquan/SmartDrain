package maintenance

import (
	"context"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Executor 的窄接口允许测试真正的“服务器接受后丢响应”。
type Executor interface {
	Submit(context.Context, *maintenancev1.Operation, maintenancev1.DrainOptions, bool) error
}

type KubernetesExecutor struct {
	Client  kubernetes.Interface
	Timeout time.Duration
}

// Submit 始终绑定原 UID；直接删除必须由外层已核对的显式选项授权。
func (e KubernetesExecutor) Submit(ctx context.Context, op *maintenancev1.Operation, opts maintenancev1.DrainOptions, dryRun bool) error {
	timeout := e.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	uid := op.UID
	del := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
	if opts.GracePeriodSeconds >= 0 {
		g := opts.GracePeriodSeconds
		del.GracePeriodSeconds = &g
	}
	if dryRun {
		del.DryRun = []string{metav1.DryRunAll}
	}
	if op.Action == "Delete" {
		return e.Client.CoreV1().Pods(op.Namespace).Delete(ctx, op.Name, del)
	}
	return e.Client.PolicyV1().Evictions(op.Namespace).Evict(ctx, &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: op.Name, Namespace: op.Namespace}, DeleteOptions: &del})
}

// RecordResult 不让后一次明确拒绝覆盖历史结果未知。
func RecordResult(op *maintenancev1.Operation, err error) {
	if err == nil {
		op.State = "Accepted"
		op.Accepted = true
		op.LastReason = "Accepted"
		return
	}
	op.LastReason = err.Error()
	if len(op.LastReason) > 2048 {
		op.LastReason = op.LastReason[:2048]
	}
	if apierrors.IsTooManyRequests(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsConflict(err) || apierrors.IsNotFound(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
		op.State = "Rejected"
		return
	}
	op.State = "Unknown"
	op.UnresolvedUnknown = true
}

// MayHaveExecuted 判断退出时是否必须继续观察原 UID。
func MayHaveExecuted(op *maintenancev1.Operation) bool {
	return op.Accepted || op.UnresolvedUnknown || op.State == "AttemptStarted" || op.State == "Accepted" || op.State == "Unknown"
}
