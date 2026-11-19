# 日志、事件与指标

代码在 `internal/observability`，调用点在请求提交之后。日志携带 taskUID、Node UID、operationID、Pod UID、action、outcome 和调用耗时，定位同名对象重建及重复请求。Events 用于状态原因变化，不用它代替持久协议。

Prometheus 指标只用 action、outcome、phase 等固定集合标签。不要把 Pod 名称、任务 UID 或错误全文做标签，否则每个任务都产生新时序。

`smartdrain_pod_operations_total` 统计 API 尝试，重试也计数，不是迁移成功数。`smartdrain_pod_operation_duration_seconds` 只测 API 调用，不包含 Pod 终止和恢复等待。`smartdrain_tasks` 从缓存统计 CR 当前阶段；`smartdrain_unresolved_operations` 表示尚未解除的请求不确定性。

指标随进程重启，不能作为审计账本。当前状态看 CR，详细时间线看结构化日志；复现实验另存原始 Pod、Node、任务与事件快照。
