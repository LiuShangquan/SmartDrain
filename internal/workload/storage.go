package workload

import (
	"context"
	"fmt"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StorageResult 为计算布局提供每个 Pod 的卷候选交集。
type StorageResult struct {
	Report  maintenancev1.CheckReport
	Volumes []maintenancev1.VolumeIdentity
	Allowed map[string]bool
}

// AnalyzeStorage 验证 NFS profile 和卷身份；其他后端报告未知而非伪造保证。
func AnalyzeStorage(ctx context.Context, r client.Reader, p *corev1.Pod, nodes []corev1.Node) (StorageResult, error) {
	out := StorageResult{Report: maintenancev1.CheckReport{Result: "Pass", Reason: "NoPersistentVolumes"}, Allowed: map[string]bool{}}
	for _, n := range nodes {
		out.Allowed[n.Name] = true
	}
	unknown := func(why string) { out.Report = maintenancev1.CheckReport{Result: "Unknown", Reason: why} }
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			switch {
			case v.ConfigMap != nil, v.Secret != nil, v.Projected != nil, v.DownwardAPI != nil, v.EmptyDir != nil:
			default:
				unknown("UnsupportedInlineVolume")
			}
			continue
		}
		pvc := &corev1.PersistentVolumeClaim{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: v.PersistentVolumeClaim.ClaimName}, pvc); err != nil {
			return out, err
		}
		if pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" || pvc.DeletionTimestamp != nil {
			unknown("PVCNotStableBound")
			continue
		}
		if len(pvc.Status.Conditions) > 0 {
			unknown("PVCConditions")
		}
		request := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		capacity := pvc.Status.Capacity[corev1.ResourceStorage]
		if request.Cmp(capacity) > 0 {
			unknown("PVCResizePending")
		}
		pv := &corev1.PersistentVolume{}
		if err := r.Get(ctx, client.ObjectKey{Name: pvc.Spec.VolumeName}, pv); err != nil {
			return out, err
		}
		ref := pv.Spec.ClaimRef
		if pv.Status.Phase != corev1.VolumeBound || pv.DeletionTimestamp != nil || ref == nil || ref.UID != pvc.UID || ref.Namespace != pvc.Namespace || ref.Name != pvc.Name {
			unknown("PVBindingMismatch")
			continue
		}
		out.Volumes = append(out.Volumes, maintenancev1.VolumeIdentity{PVC: maintenancev1.ObjectIdentity{Kind: "PersistentVolumeClaim", Namespace: pvc.Namespace, Name: pvc.Name, UID: pvc.UID}, PV: maintenancev1.ObjectIdentity{Kind: "PersistentVolume", Name: pv.Name, UID: pv.UID}, SourceHash: Hash(pv.Spec.PersistentVolumeSource)})
		if pv.Spec.NodeAffinity != nil && pv.Spec.NodeAffinity.Required != nil {
			sel, err := nodeaffinity.NewNodeSelector(pv.Spec.NodeAffinity.Required)
			if err != nil {
				return out, err
			}
			for i := range nodes {
				if !sel.Match(&nodes[i]) {
					out.Allowed[nodes[i].Name] = false
				}
			}
		}
		csi := pv.Spec.CSI
		if csi == nil || csi.Driver != "nfs.csi.k8s.io" {
			unknown("UnverifiedStorageDriver")
			continue
		}
		if (pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode != corev1.PersistentVolumeFilesystem) || (pvc.Spec.VolumeMode != nil && *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem) || !hasRWX(pv.Spec.AccessModes) || !hasRWX(pvc.Spec.AccessModes) {
			unknown("UnsupportedVolumeMode")
			continue
		}
		driver := &storagev1.CSIDriver{}
		if err := r.Get(ctx, client.ObjectKey{Name: csi.Driver}, driver); err != nil {
			return out, err
		}
		if driver.Spec.AttachRequired == nil || *driver.Spec.AttachRequired {
			unknown("NFSAttachRequired")
			continue
		}
		for _, n := range nodes {
			if !out.Allowed[n.Name] {
				continue
			}
			csinode := &storagev1.CSINode{}
			if err := r.Get(ctx, client.ObjectKey{Name: n.Name}, csinode); err != nil {
				out.Allowed[n.Name] = false
				continue
			}
			found := false
			for _, d := range csinode.Spec.Drivers {
				if d.Name == csi.Driver {
					found = true
				}
			}
			out.Allowed[n.Name] = found
		}
		if out.Report.Result == "Pass" {
			out.Report.Reason = "VerifiedNFSProfile"
		}
	}
	return out, nil
}

func hasRWX(modes []corev1.PersistentVolumeAccessMode) bool {
	for _, m := range modes {
		if m == corev1.ReadWriteMany {
			return true
		}
	}
	return false
}

// VerifyVolumes 检查绑定和后端没有被同名替换，退出不调用本门禁。
func VerifyVolumes(ctx context.Context, r client.Reader, volumes []maintenancev1.VolumeIdentity) error {
	for _, v := range volumes {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: v.PVC.Namespace, Name: v.PVC.Name}, pvc); err != nil {
			return err
		}
		pv := &corev1.PersistentVolume{}
		if err := r.Get(ctx, client.ObjectKey{Name: v.PV.Name}, pv); err != nil {
			return err
		}
		request, capacity := pvc.Spec.Resources.Requests[corev1.ResourceStorage], pvc.Status.Capacity[corev1.ResourceStorage]
		if pvc.UID != v.PVC.UID || pv.UID != v.PV.UID || pvc.Spec.VolumeName != pv.Name || pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != pvc.UID || pv.Spec.ClaimRef.Namespace != pvc.Namespace || pv.Spec.ClaimRef.Name != pvc.Name || pvc.DeletionTimestamp != nil || pv.DeletionTimestamp != nil || pvc.Status.Phase != corev1.ClaimBound || pv.Status.Phase != corev1.VolumeBound || len(pvc.Status.Conditions) > 0 || request.Cmp(capacity) > 0 || Hash(pv.Spec.PersistentVolumeSource) != v.SourceHash {
			return fmt.Errorf("StorageChanged: %s/%s", pvc.Namespace, pvc.Name)
		}
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != "nfs.csi.k8s.io" || !hasRWX(pv.Spec.AccessModes) || !hasRWX(pvc.Spec.AccessModes) || (pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode != corev1.PersistentVolumeFilesystem) || (pvc.Spec.VolumeMode != nil && *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem) {
			return fmt.Errorf("StorageProfileChanged")
		}
		driver := &storagev1.CSIDriver{}
		if err := r.Get(ctx, client.ObjectKey{Name: pv.Spec.CSI.Driver}, driver); err != nil {
			return err
		}
		if driver.Spec.AttachRequired == nil || *driver.Spec.AttachRequired {
			return fmt.Errorf("StorageProfileChanged")
		}
	}
	return nil
}
