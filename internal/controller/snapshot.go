package controller

import (
	"context"
	"fmt"
	"sort"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/planner"
	"github.com/smartdrain/smartdrain/internal/workload"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type snapshot struct {
	nodes                        []corev1.Node
	pods                         []corev1.Pod
	source                       []corev1.Pod
	reports                      []maintenancev1.PodReport
	targets                      []corev1.Pod
	blocked, waiting, activeLeft int
}

// snapshot 从 API 获取跨 namespace 观察，不伪装成跨对象事务快照。
func (r *NodeMaintenanceReconciler) snapshot(ctx context.Context, t *maintenancev1.NodeMaintenance) (snapshot, error) {
	s := snapshot{}
	if _, err := labels.Parse(t.Spec.Drain.PodSelector); err != nil {
		return s, fmt.Errorf("InvalidSelector: %w", err)
	}
	nodes := &corev1.NodeList{}
	if err := r.APIReader.List(ctx, nodes); err != nil {
		return s, err
	}
	s.nodes = nodes.Items
	found := false
	for _, n := range s.nodes {
		if n.Name == t.Spec.NodeName {
			found = true
			if t.Status.NodeUID != "" && t.Status.NodeUID != n.UID {
				return s, fmt.Errorf("NodeReplaced")
			}
			break
		}
	}
	if !found {
		return s, fmt.Errorf("NodeNotFound: %s", t.Spec.NodeName)
	}
	continuation := ""
	for {
		list := &corev1.PodList{}
		opts := &client.ListOptions{Limit: t.Spec.Drain.ChunkSize, Continue: continuation}
		if err := r.APIReader.List(ctx, list, opts); err != nil {
			return s, err
		}
		s.pods = append(s.pods, list.Items...)
		continuation = list.Continue
		if continuation == "" {
			break
		}
	}
	for i := range s.pods {
		p := &s.pods[i]
		if p.Spec.NodeName != t.Spec.NodeName {
			continue
		}
		s.source = append(s.source, *p)
	}
	sort.Slice(s.source, func(i, j int) bool {
		a, b := s.source[i], s.source[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		ao, bo := metav1.GetControllerOf(&a), metav1.GetControllerOf(&b)
		akey, bkey := "", ""
		if ao != nil {
			akey = string(ao.UID)
		}
		if bo != nil {
			bkey = string(bo.UID)
		}
		if akey != bkey {
			return akey < bkey
		}
		if ao != nil && bo != nil && ao.Kind == "StatefulSet" && bo.Kind == "StatefulSet" && ao.UID == bo.UID {
			an, ae := workload.Ordinal(&a)
			bn, be := workload.Ordinal(&b)
			if ae == nil && be == nil && an != bn {
				return an > bn
			}
		}
		return a.Name > b.Name
	})
	for i := range s.source {
		p := &s.source[i]
		report := workload.Classify(ctx, r.APIReader, p, t.Spec, r.now())
		s.reports = append(s.reports, report)
		switch report.Action {
		case workload.Block:
			s.blocked++
		case workload.Wait:
			s.waiting++
		case workload.Evict:
			s.targets = append(s.targets, *p)
		}
		if !workload.Terminal(p) && report.Action != workload.Retain {
			s.activeLeft++
		}
	}
	if len(s.reports) > maintenancev1.MaxRecords {
		return s, fmt.Errorf("ReportCapacityExceeded: %d pods", len(s.reports))
	}
	return s, nil
}

// assess 同时生成报告；只有 Guarded 把已支持模型的检查作为门禁。
func (r *NodeMaintenanceReconciler) assess(ctx context.Context, t *maintenancev1.NodeMaintenance, s snapshot, capture bool) (string, error) {
	t.Status.Pods = s.reports
	if len(s.targets) > 0 || s.waiting > 0 || s.blocked > 0 {
		r.condition(t, "DrainComplete", metav1.ConditionFalse, "TargetsRemain", "Selected targets still require processing")
	}
	if s.activeLeft > 0 || len(t.Status.PendingCleanup) > 0 {
		r.condition(t, "SourceDrained", metav1.ConditionFalse, "RemainingPods", "Current full-node observation contains active or unresolved Pods")
	}
	if err := observeJobs(t, s); err != nil {
		return "", err
	}
	if t.Spec.Drain.DisableEviction || !r.EvictionSupported {
		r.condition(t, "PDBProtection", metav1.ConditionFalse, "DirectDelete", "Delete bypasses disruption budgets; requires explicit deployment permission")
	} else {
		r.condition(t, "PDBProtection", metav1.ConditionTrue, "EvictionAPI", "Disruption decisions are made by the API server")
	}
	allowed := map[string]map[string]bool{}
	migrations := []corev1.Pod{}
	t.Status.Storage = maintenancev1.CheckReport{Result: "NotApplicable", Reason: "NoEvictTargets"}
	t.Status.Recovery = maintenancev1.CheckReport{Result: "NotApplicable", Reason: "NoReplicaRecoveryTargets"}
	problem := ""
	baselines := map[string]maintenancev1.WorkloadBaseline{}
	for _, b := range t.Status.Baselines {
		baselines[string(b.UID)] = b
	}
	for i := range s.targets {
		p := &s.targets[i]
		if workload.Terminal(p) {
			continue
		}
		migrations = append(migrations, *p)
		storage, err := workload.AnalyzeStorage(ctx, r.APIReader, p, s.nodes)
		if err != nil {
			storage.Report = maintenancev1.CheckReport{Result: "Unknown", Reason: "StorageLookup", Message: err.Error()}
		}
		allowed[string(p.UID)] = storage.Allowed
		if t.Status.Storage.Result == "NotApplicable" || storage.Report.Result != "Pass" {
			t.Status.Storage = storage.Report
		}
		if storage.Report.Result != "Pass" && problem == "" {
			problem = "StorageCheck: " + storage.Report.Reason
		}
		o := metav1.GetControllerOf(p)
		if o != nil && o.Kind == "Job" && o.APIVersion == "batch/v1" {
			if why := r.checkJob(ctx, p); why != "" && problem == "" {
				problem = why
			}
			continue
		}
		in, err := workload.Inspect(ctx, r.APIReader, p)
		if err != nil {
			t.Status.Recovery = maintenancev1.CheckReport{Result: "Unknown", Reason: "RecoveryModelUnavailable", Message: err.Error()}
			if problem == "" {
				problem = err.Error()
			}
			continue
		}
		if t.Status.Recovery.Result == "NotApplicable" {
			t.Status.Recovery = maintenancev1.CheckReport{Result: "Pass", Reason: "StableWorkloadSnapshot"}
		}
		if in.Baseline.Kind == "StatefulSet" {
			ok, err := workload.MatchingPDB(ctx, r.APIReader, p)
			if err != nil || !ok {
				t.Status.Recovery = maintenancev1.CheckReport{Result: "Unknown", Reason: "MissingPDB", Message: "Guarded StatefulSet recovery requires a matching disruption budget"}
				if problem == "" {
					problem = "MissingPDB"
				}
			}
			ordinal, e := workload.Ordinal(p)
			if e != nil || ordinal < in.Baseline.StartOrdinal || ordinal >= in.Baseline.StartOrdinal+in.Baseline.Desired {
				problem = "OrdinalOutsideBaseline"
			}
		}
		if previous, exists := baselines[string(in.Baseline.UID)]; exists {
			if workload.Hash(previous) != workload.Hash(in.Baseline) && problem == "" {
				problem = "WorkloadChanged"
			}
		} else if t.Status.BaselineCaptured {
			if problem == "" {
				problem = "WorkloadChanged: new owner"
			}
		} else if capture {
			baselines[string(in.Baseline.UID)] = in.Baseline
		}
		ok, _, why := workload.Healthy(in, s.pods, nodeMap(s.nodes))
		if !ok {
			t.Status.Recovery = maintenancev1.CheckReport{Result: "Blocked", Reason: "UnhealthyWorkload", Message: why}
		}
		if !ok && problem == "" {
			problem = why
		}
	}
	for i, rep := range s.reports {
		if rep.Action == workload.Wait {
			if why := r.checkJob(ctx, &s.source[i]); why != "" && problem == "" {
				problem = why
			}
		}
	}
	if capture && !t.Status.BaselineCaptured && problem == "" {
		t.Status.Baselines = nil
		for _, b := range baselines {
			t.Status.Baselines = append(t.Status.Baselines, b)
		}
		sort.Slice(t.Status.Baselines, func(i, j int) bool { return string(t.Status.Baselines[i].UID) < string(t.Status.Baselines[j].UID) })
		if len(t.Status.Baselines) > maintenancev1.MaxRecords {
			return "", fmt.Errorf("BaselineCapacityExceeded")
		}
		t.Status.BaselineCaptured = true
	}
	t.Status.Compute = planner.Plan(t.Spec.NodeName, s.nodes, s.pods, migrations, allowed)
	if t.Status.Compute.Result != "Pass" && t.Status.Compute.Result != "NotApplicable" && problem == "" {
		problem = "ComputeFit: " + t.Status.Compute.Reason
	}
	if s.blocked > 0 {
		for _, rep := range s.reports {
			if rep.Action == workload.Block {
				return "NativeFilter: " + rep.Reason, nil
			}
		}
	}
	if t.Spec.ExecutionPolicy == maintenancev1.PolicyCompatible {
		return "", nil
	}
	return problem, nil
}

func observeJobs(t *maintenancev1.NodeMaintenance, s snapshot) error {
	if t.Spec.EffectiveJobPolicy() != "Wait" {
		return nil
	}
	current := map[string]*corev1.Pod{}
	for i := range s.source {
		current[string(s.source[i].UID)] = &s.source[i]
	}
	seen := map[string]bool{}
	for i := range t.Status.JobObservations {
		o := &t.Status.JobObservations[i]
		seen[string(o.UID)] = true
		if p := current[string(o.UID)]; p != nil {
			if workload.Terminal(p) {
				o.Outcome = string(p.Status.Phase)
			}
		} else if o.Outcome == "Running" {
			o.Outcome = "GoneWithoutObservedResult"
		}
	}
	for i, rep := range s.reports {
		p := &s.source[i]
		o := metav1.GetControllerOf(p)
		if o == nil || o.Kind != "Job" || o.APIVersion != "batch/v1" || (rep.Action != workload.Wait && rep.Action != workload.Retain) || seen[string(p.UID)] {
			continue
		}
		if len(t.Status.JobObservations) >= maintenancev1.MaxRecords {
			return fmt.Errorf("JobObservationCapacityExceeded")
		}
		outcome := "Running"
		if workload.Terminal(p) {
			outcome = string(p.Status.Phase)
		}
		t.Status.JobObservations = append(t.Status.JobObservations, maintenancev1.JobObservation{ObjectIdentity: workload.Identity(p), Outcome: outcome})
	}
	return nil
}

func nodeMap(nodes []corev1.Node) map[string]*corev1.Node {
	m := map[string]*corev1.Node{}
	for i := range nodes {
		m[nodes[i].Name] = &nodes[i]
	}
	return m
}

func (r *NodeMaintenanceReconciler) checkJob(ctx context.Context, p *corev1.Pod) string {
	o := metav1.GetControllerOf(p)
	if o == nil {
		return "JobOwnerMissing"
	}
	j := &batchv1.Job{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: o.Name}, j); err != nil {
		return "JobLookup: " + err.Error()
	}
	if j.UID != o.UID {
		return "JobUIDChanged"
	}
	if workload.BypassesCordon(&j.Spec.Template.Spec) {
		return "IsolationUnknown: Job bypasses cordon"
	}
	return ""
}
