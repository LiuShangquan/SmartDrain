package controller

import (
	"context"
	"fmt"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/workload"
)

// allRecovered 检查冻结规格与健康容量，连续窗口只驻留内存以保证重启后重计。
func (r *NodeMaintenanceReconciler) allRecovered(ctx context.Context, t *maintenancev1.NodeMaintenance, s snapshot, stable bool) string {
	firstReason := ""
	for _, b := range t.Status.Baselines {
		key := string(t.UID) + "/" + string(b.UID)
		in, err := workload.Reload(ctx, r.APIReader, b)
		if err != nil {
			r.forgetStable(key)
			if firstReason == "" {
				firstReason = err.Error()
			}
			continue
		}
		ok, fingerprint, why := workload.Healthy(in, s.pods, nodeMap(s.nodes))
		if !ok {
			r.forgetStable(key)
			if firstReason == "" {
				firstReason = why
			}
			continue
		}
		if stable && !r.isStable(key, fingerprint, time.Duration(b.MinReadySeconds)*time.Second) {
			if firstReason == "" {
				firstReason = "RecoveryStabilizing"
			}
		}
	}
	if len(t.Status.Baselines) > 0 {
		t.Status.Recovery = maintenancev1.CheckReport{Result: "Pass", Reason: "RecoveryVerified"}
		if firstReason != "" {
			t.Status.Recovery = maintenancev1.CheckReport{Result: "Blocked", Reason: reasonToken(firstReason), Message: firstReason}
		}
	}
	return firstReason
}

// operationRecovered 对 StatefulSet 额外验证同 ordinal 的新 UID 和卷映射。
func (r *NodeMaintenanceReconciler) operationRecovered(ctx context.Context, t *maintenancev1.NodeMaintenance, s snapshot) string {
	op := t.Status.CurrentOperation
	if err := workload.VerifyVolumes(ctx, r.APIReader, op.Volumes); err != nil {
		return err.Error()
	}
	if op.RecoveryKind == "StatefulOrdinalReady" {
		found := false
		for i := range s.pods {
			p := &s.pods[i]
			if p.Namespace != op.Namespace || p.Name != op.Name || p.UID == op.UID {
				continue
			}
			if p.Spec.NodeName == t.Spec.NodeName {
				return "ReplacementOnSourceNode"
			}
			in, err := workload.Inspect(ctx, r.APIReader, p)
			if err != nil {
				return err.Error()
			}
			if in.Baseline.UID != op.OwnerUID {
				return "ReplacementOwnerChanged"
			}
			ordinal, err := workload.Ordinal(p)
			if err != nil || ordinal != op.Ordinal {
				return "ReplacementOrdinalChanged"
			}
			storage, err := workload.AnalyzeStorage(ctx, r.APIReader, p, s.nodes)
			if err != nil {
				return err.Error()
			}
			if workload.Hash(storage.Volumes) != workload.Hash(op.Volumes) {
				return "ReplacementVolumeChanged"
			}
			found = true
			break
		}
		if !found {
			return "WaitingForOrdinalReplacement"
		}
	}
	if why := r.allRecovered(ctx, t, s, true); why != "" {
		return why
	}
	if op.OwnerUID == "" {
		return fmt.Sprintf("MissingRecoveryOwner: %s", op.Name)
	}
	return ""
}

func (r *NodeMaintenanceReconciler) isStable(key, fingerprint string, minimum time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	window := r.StabilityWindow
	if window == 0 {
		window = 10 * time.Second
	}
	if minimum > window {
		window = minimum
	}
	if r.stable == nil {
		r.stable = map[string]stableObservation{}
	}
	old, ok := r.stable[key]
	if !ok || old.fingerprint != fingerprint {
		r.stable[key] = stableObservation{fingerprint: fingerprint, since: r.now()}
		return false
	}
	return r.now().Sub(old.since) >= window
}
func (r *NodeMaintenanceReconciler) forgetStable(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.stable, key)
}
