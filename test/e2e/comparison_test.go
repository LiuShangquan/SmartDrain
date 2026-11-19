package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/workload"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type comparisonSample struct {
	Seconds       float64 `json:"seconds"`
	Ready, Source int
	HTTPReady     bool
}
type comparisonResult struct {
	Policy, PDB, Namespace                 string
	Round                                  int
	SourceDrainedSeconds, RecoveredSeconds float64
	MinimumReady, Requests, FailedRequests int
	Outcome                                string
	Samples                                []comparisonSample
}

// 相同副本、初始分布和探针下使用共同外部终点；超时明确记为未完成。
func TestRepeatedDrainComparison(t *testing.T) {
	if os.Getenv("SMARTDRAIN_COMPARISON") != "1" {
		t.Skip("explicit comparison suite required")
	}
	for round := 1; round <= 3; round++ {
		for _, pdbMode := range []string{"none", "permissive", "strict"} {
			for _, policy := range []string{"kubectl", maintenancev1.PolicyCompatible, maintenancev1.PolicyGuarded} {
				t.Run(fmt.Sprintf("%s/%s/run-%d", pdbMode, policy, round), func(t *testing.T) {
					e := setup(t)
					e.deployment("web", 2, "4")
					service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: e.namespace}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}, Ports: []corev1.ServicePort{{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080)}}}}
					if err := e.c.Create(e.ctx, service); err != nil {
						t.Fatal(err)
					}
					if pdbMode != "none" {
						min := intstr.FromInt32(1)
						if pdbMode == "strict" {
							min = intstr.FromInt32(2)
						}
						pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: e.namespace}, Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &min, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}
						if err := e.c.Create(e.ctx, pdb); err != nil {
							t.Fatal(err)
						}
						e.until("comparison-pdb-observed", time.Minute, func() bool {
							_ = e.c.Get(e.ctx, client.ObjectKeyFromObject(pdb), pdb)
							return pdb.Status.CurrentHealthy == 2 && pdb.Status.ObservedGeneration == pdb.Generation
						})
					}
					cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
					if err != nil {
						t.Fatal(err)
					}
					cs, err := kubernetes.NewForConfig(cfg)
					if err != nil {
						t.Fatal(err)
					}
					probe := func() bool {
						ctx, cancel := context.WithTimeout(e.ctx, 800*time.Millisecond)
						defer cancel()
						_, err := cs.CoreV1().Services(e.namespace).ProxyGet("http", "web", "8080", "ready", nil).DoRaw(ctx)
						return err == nil
					}
					e.until("comparison-service-ready", time.Minute, probe)
					result := comparisonResult{Policy: policy, PDB: pdbMode, Namespace: e.namespace, Round: round, MinimumReady: 2, Outcome: "NotCompleted"}
					started := time.Now()
					limit := 50 * time.Second
					timeoutSeconds := int64(60)
					if pdbMode == "strict" {
						limit = 32 * time.Second
						timeoutSeconds = 25
					}
					var task *maintenancev1.NodeMaintenance
					nativeDone := make(chan error, 1)
					if policy == "kubectl" {
						binary := os.Getenv("SMARTDRAIN_KUBECTL")
						if binary == "" {
							t.Fatal("set SMARTDRAIN_KUBECTL to verified v1.35.0 binary")
						}
						cmd := exec.CommandContext(e.ctx, binary, "--kubeconfig", os.Getenv("KUBECONFIG"), "drain", e.source, "--ignore-daemonsets", fmt.Sprintf("--timeout=%ds", timeoutSeconds))
						go func() {
							output, err := cmd.CombinedOutput()
							dir := filepath.Join("..", "..", "artifacts", "runs", "comparison")
							_ = os.MkdirAll(dir, 0755)
							_ = os.WriteFile(filepath.Join(dir, e.namespace+"-kubectl.txt"), output, 0644)
							nativeDone <- err
						}()
						t.Cleanup(func() { e.cordon(e.source, false) })
					} else {
						task = e.task("comparison", policy, func(t *maintenancev1.NodeMaintenance) { t.Spec.TimeoutSeconds = timeoutSeconds })
					}
					var healthySince time.Time
					for time.Since(started) < limit {
						pods := &corev1.PodList{}
						if err := e.c.List(e.ctx, pods, client.InNamespace(e.namespace), client.MatchingLabels{"app": "web"}); err != nil {
							t.Fatal(err)
						}
						ready, source := 0, 0
						for _, p := range pods.Items {
							if workload.PodReady(&p) {
								ready++
							}
							if p.Spec.NodeName == e.source && !workload.Terminal(&p) {
								source++
							}
						}
						now := time.Now()
						ok := probe()
						result.Requests++
						if !ok {
							result.FailedRequests++
						}
						if ready < result.MinimumReady {
							result.MinimumReady = ready
						}
						result.Samples = append(result.Samples, comparisonSample{Seconds: now.Sub(started).Seconds(), Ready: ready, Source: source, HTTPReady: ok})
						if source == 0 && result.SourceDrainedSeconds == 0 {
							result.SourceDrainedSeconds = now.Sub(started).Seconds()
						}
						if source == 0 && ready == 2 && ok {
							if healthySince.IsZero() {
								healthySince = now
							}
							if now.Sub(healthySince) >= 10*time.Second {
								result.RecoveredSeconds = now.Sub(started).Seconds()
								result.Outcome = "Recovered"
								break
							}
						} else {
							healthySince = time.Time{}
						}
						time.Sleep(500 * time.Millisecond)
					}
					if policy == "kubectl" {
						select {
						case err := <-nativeDone:
							if pdbMode != "strict" && err != nil {
								t.Fatal(err)
							}
						case <-time.After(time.Second):
							t.Fatal("native drain process did not exit")
						}
					}
					e.dump("comparison-end")
					dir := filepath.Join("..", "..", "artifacts", "runs", "comparison")
					_ = os.MkdirAll(dir, 0755)
					bytes, _ := json.MarshalIndent(result, "", "  ")
					if err := os.WriteFile(filepath.Join(dir, e.namespace+".json"), bytes, 0644); err != nil {
						t.Fatal(err)
					}
					t.Logf("policy=%s pdb=%s round=%d outcome=%s source=%.2fs recovery=%.2fs minReady=%d failedRequests=%d/%d", policy, pdbMode, round, result.Outcome, result.SourceDrainedSeconds, result.RecoveredSeconds, result.MinimumReady, result.FailedRequests, result.Requests)
					if pdbMode == "strict" {
						if result.SourceDrainedSeconds > 0 || result.MinimumReady != 2 {
							t.Fatal("strict PDB was bypassed")
						}
					} else if result.Outcome != "Recovered" {
						t.Fatal("did not meet common recovery endpoint")
					}
					if task != nil {
						e.restore(task)
					}
				})
			}
		}
	}
}
