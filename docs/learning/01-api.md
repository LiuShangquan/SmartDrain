# API 与任务契约

入口是 `api/v1alpha1/nodemaintenance_types.go`。`NodeMaintenanceSpec` 保存用户意图，`NodeMaintenanceStatus` 保存控制器观察和恢复凭据。Node 是集群级对象，因此维护 CR 也使用 cluster scope。

`NodeName` 是可读名称，`status.nodeUID` 才是对象身份。同名 Node 删除后再创建会得到新的 UID，旧任务不能操作新节点。Pod、owner、PVC 和 PV 也遵循相同原则。

`executionPolicy` 创建后不可变：Guarded 执行额外容量和恢复门禁；DrainCompatible 保留原生处理能力。`jobPolicy=Default` 解析为各策略的默认值，最终值写入 status。`desiredState` 只能从 Maintenance 进入 Restored，不能复用已退出任务偷偷开始新维护。

结构体上的 `+kubebuilder` 注释由 controller-gen 生成 CRD schema。CEL 在 API Server 端拒绝非法更新，比只在客户端检查可靠。Go 的零值与 JSON 的 `omitempty` 必须谨慎：宽限期 0 是明确立即终止意图，不能因为编码时省略而变成默认 -1。

字段是否必须由用户填写，与 Go 编码时是否保留零值是两件事。有服务端默认值的字段用 `+optional` 标记为可省略，同时保留不带 `omitempty` 的零值编码。这样精简 YAML 能通过 kubectl 校验，Go 客户端显式提交的 0 也不会被替换成默认值；两种入口都用真实 API 验证。

`metav1.Condition` 将互相独立的事实分开：DrainComplete、SourceDrained、WorkloadRecovered、MaintenanceReady 不应合成一个笼统的成功字段。完成目标子集、跳过 terminating 等待或业务恢复未知，都不能被改写成整个节点维护安全。

阅读练习：追踪一个带 `timeoutSeconds: 0` 的 CR；解释为什么重启后不能重新计算一个新的超时起点；再尝试修改不可变 nodeName，观察 API Server 的拒绝。
