# 控制器：把事实组织成可恢复流程

入口是 `internal/controller/nodemaintenance_controller.go`。它依赖 Kubernetes 客户端、直读 API 的 Reader、窄 Executor 接口、时钟和 EventRecorder；`cmd/main.go` 只负责装配。

## 一次协调解决什么

先读取 CR 最新意图，优先处理 Restored 和删除。存在 currentOperation 时先观察旧 UID，再判断是否允许重试。没有在途请求才检查暂停、期限、全局槽、完整快照、资源与工作负载门禁。每轮只推进一个持久化步骤，然后返回 RequeueAfter；不在 Reconcile 内用 sleep 等副本。

正常顺序是 finalizer → Lease → 开始时间与 Node UID → 预检 → claim+cordon → Prepared → AttemptStarted → Eviction → 旧 UID 消失 → 恢复窗口。每个箭头都是可能崩溃的位置。Pod、Node、PDB、控制器和存储对象变化会唤醒任务；周期重查兜底。

## 为什么有四个结果

- DrainComplete：所选目标按所选等待规则处理完了。
- SourceDrained：全量扫描下没有待迁移或待自然结束的活动业务。
- WorkloadRecovered：增强模型验证的恢复结果；兼容模式保持 Unknown。
- MaintenanceReady：当前策略要求全部满足，才能开始外部维护。

选择器只选部分 Pod、skip-wait 留下删除中的实例时，前一个结果可能为 True，后几个仍不能冒充成功。Maintenance 期间继续观察，新业务到达会使维护许可失效。

## 持久状态和内存状态

Pod UID、请求开始标记、未知结果、工作负载基线和清理凭据写入 status。健康稳定窗口只在内存中保存：重启后重新观察至少 10 秒。这会增加等待，但不会把重启前后两个不连续的健康片段拼起来。

一次单测发现了实际实现问题：`Status().Update` 会用 API 响应重填对象，更新前取得的 `op := task.Status.CurrentOperation` 可能指向旧副本。写完 AttemptStarted 后必须重新取得嵌套指针，否则请求结果没有写回 status。这也是测试要穿过真实客户端行为，而不只检查辅助函数的原因。

## 自己追一遍

运行控制器单测后，跟踪 `TestHistoricalUnknownPauseAndExit`：响应未知 → 暂停禁止重试 → Restored 仍观察旧 UID → 旧 UID 消失 → Node 清理 → 释放 Lease。解释为什么退出不等待新副本 Ready，以及为什么超时不能自动解除 cordon。
