# 项目经历写法

项目名称可写：**SmartDrain：Kubernetes 节点维护 Operator**。定位为个人研发项目，技术栈 Go、controller-runtime、client-go、CRD/CEL、Kustomize、kind、Prometheus、NFS CSI。

## 可使用的描述

- 设计并实现节点维护 CRD 与声明式协调流程，在保留原生 drain 对象覆盖及显式选项的基础上，提供 Guarded 与 DrainCompatible 两种执行策略，覆盖 Deployment、RS/RC、StatefulSet、Job/CronJob 和通用 Pod 分类。
- 基于有效 requests、节点硬约束与卷候选交集实现容量预检；通过 owner UID、冻结规格、StatefulSet ordinal/revision 和连续健康窗口控制迁移推进，区分节点排空、替代副本恢复与 Job 实例结果。
- 实现持久任务 Lease、原子 Node claim、带 Pod UID 前置条件的 Eviction 和请求结果未知恢复协议；验证写入中断、API 接受后丢响应、退出清理及 NFS 跨节点数据读回。
- 将 144 个筛选组合直接与固定版本 kubectl 过滤器对照，并完成 27 组真实集群对照和三轮资源碎片实验，保存逐次结果与原始快照。

简历空间有限时保留前三条，把测试规模放在第三条末尾。不要写生产集群规模、线上业务收益或没有测过的吞吐量提升。

## 面试时用证据展开

对照结果中，无 PDB、两个副本和 4 秒就绪延迟场景下，三轮 Guarded 的健康副本最低点为 1，稀疏 HTTP 探测未观察到失败；代价是共同恢复时间中位数约 32 秒。原生 drain 配置允许一次中断的 PDB 后，同样保住一个副本并未观察到探测失败。项目价值应解释为额外预检、持续恢复门禁和持久清理协议，不把 PDB 本来具备的保护归为项目创新。

准备讲清楚四个问题：

1. 为什么合计 800m 的余量不能容纳一个 600m Pod？为什么贪心失败不总是 Blocked？
2. 为什么服务器返回成功、旧 UID 消失和新副本 Ready 必须分开判断？
3. 为什么结果未知后再收到 429 仍不能解除清理责任？
4. 为什么同名 StatefulSet Pod、Ready 和 NFS 文件读回仍不等于数据库一致性？

实现、运行与验证细节分别对应 design.md、runbook.md、experiments.md；实际数据以 artifacts/results 为准。
