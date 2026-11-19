// Package observability 提供低基数指标；对象身份保留在日志和 CR status 中。
package observability

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	Attempts        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smartdrain_pod_operations_total", Help: "Pod API attempts by action and observed outcome; retries are counted."}, []string{"action", "outcome"})
	RequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "smartdrain_pod_operation_duration_seconds", Help: "Pod API request duration, excluding recovery wait.", Buckets: prometheus.DefBuckets}, []string{"action"})
)

func init() { metrics.Registry.MustRegister(Attempts, RequestDuration) }

// TaskCollector 从缓存收集当前任务数量，不以 task/node/pod 名称创建标签。
type TaskCollector struct {
	Reader                   client.Reader
	phases, unknown, success *prometheus.Desc
}

func NewTaskCollector(reader client.Reader) *TaskCollector {
	return &TaskCollector{Reader: reader,
		phases:  prometheus.NewDesc("smartdrain_tasks", "Current NodeMaintenance objects by phase.", []string{"phase"}, nil),
		unknown: prometheus.NewDesc("smartdrain_unresolved_operations", "Operations with unresolved request outcomes.", nil, nil),
		success: prometheus.NewDesc("smartdrain_status_collection_success", "Whether cached task status could be collected.", nil, nil)}
}

func (c *TaskCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.phases
	ch <- c.unknown
	ch <- c.success
}
func (c *TaskCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tasks := &maintenancev1.NodeMaintenanceList{}
	if err := c.Reader.List(ctx, tasks); err != nil {
		ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, 0)
		return
	}
	phases := map[string]float64{"Pending": 0, "Preparing": 0, "Draining": 0, "Holding": 0, "Maintenance": 0, "Restoring": 0, "Completed": 0, "Failed": 0, "Unknown": 0}
	var unknown float64
	for _, t := range tasks.Items {
		phase := t.Status.Phase
		if phase == "" {
			phase = "Pending"
		}
		if _, ok := phases[phase]; !ok {
			phase = "Unknown"
		}
		phases[phase]++
		ops := append([]maintenancev1.Operation{}, t.Status.PendingCleanup...)
		if t.Status.CurrentOperation != nil {
			ops = append(ops, *t.Status.CurrentOperation)
		}
		for _, op := range ops {
			if op.UnresolvedUnknown || op.State == "AttemptStarted" {
				unknown++
			}
		}
	}
	for phase, count := range phases {
		ch <- prometheus.MustNewConstMetric(c.phases, prometheus.GaugeValue, count, phase)
	}
	ch <- prometheus.MustNewConstMetric(c.unknown, prometheus.GaugeValue, unknown)
	ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, 1)
}
