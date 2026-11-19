# 实现与验证记录

本项目在专属 `smartdrain-dev` kind 集群验证，使用 Kubernetes/kubectl v1.35.0、一个控制面和三个 worker。以下区分实现、实测与适用边界；原始输出保存在当前工作区 `artifacts/runs/`。

## 已实现模块

| 模块 | 内容 | 学习入口 |
| --- | --- | --- |
| API | NodeMaintenance CRD、默认值、CEL、不可变选项、状态与退出契约 | [API](learning/01-api.md) |
| 工作负载 | 原生对象分类、owner UID、冻结规格、健康窗口、Job 观察、PVC/PV 身份与 NFS 候选 | [工作负载](learning/02-workload.md) |
| 容量 | 有效 requests、Pod 槽位、节点硬约束、虚拟扣减、Pass/Blocked/Unknown | [容量](learning/03-planner.md) |
| 持久协议 | 任务 Lease、原子 claim/cordon、请求日志、UID 前置条件、未知响应、退出清理 | [协议](learning/04-maintenance.md) |
| 控制器 | 两种策略、PlanOnly、暂停/超时/退出、持续观察、故障重放、显式 Delete 授权 | [控制器](learning/05-controller.md) |
| 运行与观测 | 最小默认 RBAC、部署脚本、批量创建脚本、Events、结构化日志、Prometheus 指标 | [可观测性](learning/06-observability.md) |
| 验证 | 单测、真实 API、真实工作负载、故障注入、NFS、原生 drain 对照 | [测试](learning/07-tests.md) |

## 实测证据

| 验证 | 结果与观察 | 证据 |
| --- | --- | --- |
| Go 单元测试与静态检查 | `go test -count=1 ./...` 与 `go vet ./...` 通过；普通包测试跳过真实集群用例 | 最终检查记录 |
| 原生分类一致性 | 144 个组合直接运行上游 v0.35.0 drain 过滤器与本项目实现，结果一致 | `internal/workload/native_compatibility_test.go` |
| 持久写入故障 | 14 个写入位置分别模拟写入成功后丢失响应，重建协调器并验证退出清理 | `internal/controller/crash_test.go` |
| 真实 API | CRD 默认值与显式零值、CEL、不可变字段、status 冲突、同名新 UID、默认权限、kubectl 示例通过 | `artifacts/runs/integration-final.txt` |
| 副本与 Job | Deployment、独立 RS、RC、StatefulSet 恢复；Job 自然完成、失败、超时与退出 | `artifacts/runs/e2e-core.txt`、最终回归日志 |
| 原生选项 | force、emptyDir、selector、skip-wait、server dry-run；部分排空不冒充整节点就绪 | `artifacts/runs/e2e-storage-options.txt`、最终回归日志 |
| 请求未知 | 真实 API 接受 Eviction 后注入 EOF，重建协调器；退出仍承担旧 UID 清理责任 | `artifacts/runs/e2e-faults.txt`、最终回归日志 |
| NFS | 实际 NFSv4 服务端与 NFS CSI；Deployment、StatefulSet 跨节点后读取相同文件 token | `artifacts/runs/e2e-storage-options.txt`、最终回归日志 |
| 本地 PVC | Guarded 报告未知；兼容模式实际驱逐，替代 Pod 因原节点绑定 Pending，恢复仍 Unknown | `artifacts/runs/e2e-capacity-local.txt` |
| 资源碎片 | 三条路径各三轮；两个节点各余 400m 无法接收 600m Pod。Guarded 事前阻塞，另外两条路径驱逐后 Pending | `artifacts/runs/e2e-capacity-local.txt` |
| 27 组对照 | 18 组达到共同恢复终点；9 组严格 PDB 场景未完成，均未绕过预算 | [逐次汇总](../artifacts/results/comparison-summary.md)、`artifacts/runs/e2e-comparison.txt` |

最终构建、混合对象、直接 Delete 权限切换、关键用例重复次数、race 和环境收尾的结果，统一记录在 [最终验收](../artifacts/results/verification.md)。对照数据绑定单独记录的镜像；之后的功能修正由最终回归验证，不将之前的时间数据宣称为新构建的重新测量。

## 验证中修正的问题

- status 更新会重填对象，旧的嵌套操作指针可能失效；写入 AttemptStarted 后重新取得 currentOperation，再记录请求结果。
- Node 准备意图同时保存 resourceVersion，区分“尚未提交”和“凭据消失但结果不明”；无法证明时停止，保留人工处置入口。
- 全部工作负载每轮更新健康窗口，避免前一个基线返回等待后，后续基线错误累计连续健康时间。
- 维护中发现新 Pod 时撤销旧的排空/就绪条件，重新检查；不会沿用过期成功状态。
- 默认字段显式标记为可选，同时保留 Go 零值编码；修复精简 YAML 被 kubectl 判定缺少必填字段的问题，并加入真实命令行示例验证。
- 每个集群案例等待 namespace 真正删除，避免上一个案例的 terminating Pod 污染下一次全节点扫描。真实 API 冲突测试用重试处理预期更新，仅对刻意保留的旧版本断言冲突。

## 适用边界

这是可以复现的个人研发项目。kind 节点共享宿主机；HTTP 是低频可用性探测，不能推导生产吞吐量、真实请求失败率或业务收益。容量快照不是资源预留；Pod Ready、NFS 文件 token 读回不证明数据库一致性。其他 CSI 后端、复杂调度约束明确报告 Unknown，不虚构恢复保证。

GitHub Actions 配置已提供，但没有远程执行结果。没有额外运行 envtest，本次契约验证直接使用真实 API Server。默认部署不授予 Pod Delete；仅显式启用独立选项、开关和 RBAC 时允许该路径。
