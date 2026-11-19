// Package maintenance 提供可重入的任务槽、Node 归属和删除副作用适配。
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/workload"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const SlotName = "smartdrain-task-slot"

// Claim 是与 cordon 同一次 Node patch 写入的恢复凭据。
type Claim struct {
	TaskUID               types.UID `json:"taskUID"`
	NodeUID               types.UID `json:"nodeUID"`
	OriginalUnschedulable bool      `json:"originalUnschedulable"`
	State                 string    `json:"state"`
	Result                string    `json:"result,omitempty"`
}

// Ownership 的读取直连 API，写入均携带 resourceVersion 乐观锁。
type Ownership struct {
	Reader    client.Reader
	Writer    client.Client
	Namespace string
}

// Acquire 持久占用全局任务槽，不依据 TTL 抢占其他任务。
func (o Ownership) Acquire(ctx context.Context, t *maintenancev1.NodeMaintenance, now time.Time) (bool, time.Time, error) {
	now = now.Truncate(time.Microsecond)
	l := &coordinationv1.Lease{}
	key := client.ObjectKey{Namespace: o.Namespace, Name: SlotName}
	err := o.Reader.Get(ctx, key, l)
	if apierrors.IsNotFound(err) {
		holder := string(t.UID)
		at := metav1.NewMicroTime(now)
		l = &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: SlotName, Namespace: o.Namespace}, Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, AcquireTime: &at}}
		err = o.Writer.Create(ctx, l)
		if apierrors.IsAlreadyExists(err) {
			return false, time.Time{}, nil
		}
		return err == nil, now, err
	}
	if err != nil {
		return false, time.Time{}, err
	}
	if l.Spec.HolderIdentity != nil && *l.Spec.HolderIdentity != "" && *l.Spec.HolderIdentity != string(t.UID) {
		return false, time.Time{}, nil
	}
	if l.Spec.HolderIdentity != nil && *l.Spec.HolderIdentity == string(t.UID) {
		if l.Spec.AcquireTime == nil {
			return false, time.Time{}, fmt.Errorf("LeaseMissingAcquireTime")
		}
		return true, l.Spec.AcquireTime.Time, nil
	}
	base := l.DeepCopy()
	holder := string(t.UID)
	at := metav1.NewMicroTime(now)
	l.Spec.HolderIdentity = &holder
	l.Spec.AcquireTime = &at
	err = o.Writer.Patch(ctx, l, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	return err == nil, now, err
}

// Release 只清空自有任务槽，不删除 Lease 或修改陌生持有者。
func (o Ownership) Release(ctx context.Context, t *maintenancev1.NodeMaintenance) error {
	l := &coordinationv1.Lease{}
	if err := o.Reader.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: SlotName}, l); err != nil {
		return client.IgnoreNotFound(err)
	}
	if l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity != string(t.UID) {
		return nil
	}
	base := l.DeepCopy()
	empty := ""
	l.Spec.HolderIdentity = &empty
	l.Spec.AcquireTime = nil
	return o.Writer.Patch(ctx, l, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// ReadClaim 拒绝损坏或未知格式，不能将解析失败当成没有归属。
func ReadClaim(n *corev1.Node) (*Claim, error) {
	raw := n.Annotations[maintenancev1.ClaimAnnotation]
	if raw == "" {
		return nil, nil
	}
	c := &Claim{}
	if err := json.Unmarshal([]byte(raw), c); err != nil {
		return nil, fmt.Errorf("InvalidClaim: %w", err)
	}
	if c.TaskUID == "" || c.NodeUID == "" || (c.State != "active" && c.State != "cleanupApplied") {
		return nil, fmt.Errorf("InvalidClaim")
	}
	return c, nil
}

func writeClaim(n *corev1.Node, c *Claim) {
	if n.Annotations == nil {
		n.Annotations = map[string]string{}
	}
	b, _ := json.Marshal(c)
	n.Annotations[maintenancev1.ClaimAnnotation] = string(b)
}

// Cordon 原子保存原始布尔状态和 claim，防止崩溃后丢失原值。
func (o Ownership) Cordon(ctx context.Context, t *maintenancev1.NodeMaintenance) error {
	n := &corev1.Node{}
	if err := o.Reader.Get(ctx, client.ObjectKey{Name: t.Spec.NodeName}, n); err != nil {
		return err
	}
	if n.UID != t.Status.NodeUID {
		return fmt.Errorf("NodeReplaced")
	}
	c, err := ReadClaim(n)
	if err != nil {
		return err
	}
	if c != nil {
		if c.TaskUID != t.UID || c.NodeUID != n.UID {
			return fmt.Errorf("NodeClaimedByAnotherTask")
		}
		if c.State != "active" {
			return fmt.Errorf("CleanupAlreadyStarted")
		}
		if !n.Spec.Unschedulable {
			return fmt.Errorf("IsolationLost")
		}
		return nil
	}
	if t.Status.NodeClaimResourceVersion != "" && n.ResourceVersion != t.Status.NodeClaimResourceVersion {
		return apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, n.Name, fmt.Errorf("claim preparation version changed"))
	}
	base := n.DeepCopy()
	c = &Claim{TaskUID: t.UID, NodeUID: n.UID, OriginalUnschedulable: n.Spec.Unschedulable, State: "active"}
	writeClaim(n, c)
	n.Spec.Unschedulable = true
	return o.Writer.Patch(ctx, n, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// Permit 在每次真实提交前再次核对槽位与 Node 归属。
func (o Ownership) Permit(ctx context.Context, t *maintenancev1.NodeMaintenance) error {
	l := &coordinationv1.Lease{}
	if err := o.Reader.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: SlotName}, l); err != nil {
		return err
	}
	if l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity != string(t.UID) {
		return fmt.Errorf("TaskSlotLost")
	}
	n := &corev1.Node{}
	if err := o.Reader.Get(ctx, client.ObjectKey{Name: t.Spec.NodeName}, n); err != nil {
		return err
	}
	c, err := ReadClaim(n)
	if err != nil {
		return err
	}
	if n.UID != t.Status.NodeUID || c == nil || c.TaskUID != t.UID || c.NodeUID != n.UID || c.State != "active" || !n.Spec.Unschedulable {
		return fmt.Errorf("NodeOwnershipLost")
	}
	return nil
}

// ApplyCleanup 应用 Node 清理并留下凭据，调用者须再持久化 status。
func (o Ownership) ApplyCleanup(ctx context.Context, t *maintenancev1.NodeMaintenance) (string, error) {
	n := &corev1.Node{}
	err := o.Reader.Get(ctx, client.ObjectKey{Name: t.Spec.NodeName}, n)
	if apierrors.IsNotFound(err) {
		return "NodeGone", nil
	}
	if err != nil {
		return "", err
	}
	if t.Status.NodeUID != "" && n.UID != t.Status.NodeUID {
		return "NodeReplaced", nil
	}
	c, err := ReadClaim(n)
	if err != nil {
		return "", err
	}
	if c == nil {
		if t.Status.NodeCleanupConfirmed {
			return t.Status.CleanupResult, nil
		}
		if !t.Status.NodeClaimStarted {
			return "NoNodeMutation", nil
		}
		return "", fmt.Errorf("OwnershipLost")
	}
	if c.TaskUID != t.UID || c.NodeUID != n.UID {
		return "", fmt.Errorf("OwnershipLost")
	}
	if c.State == "cleanupApplied" {
		return c.Result, nil
	}
	base := n.DeepCopy()
	result := "Restored"
	switch {
	case c.OriginalUnschedulable:
		result = "OriginallyCordoned"
	case n.Annotations[maintenancev1.HoldAnnotation] == "true":
		result = "HeldCordoned"
	case !workload.NodeReady(n):
		return "", fmt.Errorf("NodeNotReady")
	default:
		n.Spec.Unschedulable = false
	}
	c.State = "cleanupApplied"
	c.Result = result
	writeClaim(n, c)
	return result, o.Writer.Patch(ctx, n, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// RemoveClaim 只移除已确认清理完成的自有凭据。
func (o Ownership) RemoveClaim(ctx context.Context, t *maintenancev1.NodeMaintenance) error {
	n := &corev1.Node{}
	if err := o.Reader.Get(ctx, client.ObjectKey{Name: t.Spec.NodeName}, n); err != nil {
		return client.IgnoreNotFound(err)
	}
	if n.UID != t.Status.NodeUID {
		return nil
	}
	c, err := ReadClaim(n)
	if err != nil {
		return err
	}
	if c == nil {
		return nil
	}
	if c.TaskUID != t.UID || c.NodeUID != n.UID || c.State != "cleanupApplied" {
		return fmt.Errorf("OwnershipLost")
	}
	base := n.DeepCopy()
	delete(n.Annotations, maintenancev1.ClaimAnnotation)
	return o.Writer.Patch(ctx, n, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}
