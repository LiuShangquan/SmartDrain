# 持久维护协议

`ownership.go` 管理两种不同归属：固定 Lease 表示哪个维护任务占用全局槽位，Node claim 表示本任务修改了哪个 Node、原始 unschedulable 是什么。manager 选主是第三个机制，不能替代任务占用，也不提供严格 fencing。

Node claim 与 cordon 在同一次带 resourceVersion 的 patch 中写入。如果节点修改成功但 CR status 写回失败，重启后还能读出真正原始状态。`client.MergeFromWithOptimisticLock` 避免覆盖并发修改；冲突意味着重新读取，不是无条件重放旧对象。

退出分三步保存证据：Node 应用清理并留下 cleanupApplied，CR 确认 nodeCleanupConfirmed，再移除 claim 和释放槽位。每个写入之间都可能崩溃，因此不能仅凭“claim 不存在”认定恢复成功。

`executor.go` 是窄副作用接口。Prepared 先记录目标，AttemptStarted 在调用前持久化。客户端超时不意味着 API 没执行；后一次 429 也无法否定前一次请求。unresolvedUnknown 必须保留，直到原 UID 消失等证据解决不确定性。

所有 Eviction/Delete 都带原 Pod UID 前置条件。正常 PDB 拒绝不得触发 Delete 回退。直接 Delete 是显式兼容选项并需要独立 RBAC，不能作为错误处理捷径。

阅读练习：在真实 API 已接受请求后让客户端返回超时，再重启控制器；解释为什么同名新 Pod 绝不能成为重试对象。
