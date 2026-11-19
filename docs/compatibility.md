# API 与 drain 兼容矩阵

基线为 Kubernetes/kubectl **v1.35.0**；过滤器直接对照 `k8s.io/kubectl@v0.35.0/pkg/drain`。144 个类型、终态与选项组合用同一输入运行上游与本项目实现。真实命令行对照使用经官方 SHA256 校验的 v1.35.0 二进制。

## 对象范围

| Pod 来源 | DrainCompatible | Guarded 增强 |
| --- | --- | --- |
| Deployment → RS | Eviction | 冻结 Deployment 与活动 RS，健康容量与连续窗口 |
| 独立 RS / RC | Eviction | UID、规格与健康容量 |
| StatefulSet | 不因 PVC、更新策略或缺少 PDB 裁剪驱逐能力 | 稳定 OrderedReady、RollingUpdate partition=0、同 revision；要求匹配 PDB；同 ordinal 与卷身份恢复 |
| Job / CronJob 的 Job | 默认 Evict；显式 Wait 也可用 | 默认 Wait；显式 Evict 也可用，不以 Job 成功作为 drain 终点 |
| 未知 controller owner | 原生允许的目标可驱逐 | RecoveryUnknown 阻塞，不猜测副本重建 |
| 裸 Pod | 默认阻塞，force 放行 | 没有可验证恢复责任时仍报告未知 |
| 有效活动 DS | 默认阻塞；ignoreDaemonSets 时保留 | 同左 |
| 孤儿 DS | 按原生规则使用 force | 不伪造重建保证 |
| 镜像 Pod | 保留 | 保留并列出驻留对象 |
| 终态 Pod | 原生删除/驱逐路径 | 不等 Ready；自然等待 Job 的终态保留并记录结果 |

原生 unreplicatedFilter 检查 controller reference 是否存在，不是固定 Kind 白名单。DaemonSet 的读取、终态和孤儿行为另有顺序；同名 DS 的 UID 差异在过滤兼容测试中保留上游行为，不能把恢复身份策略偷换成原生筛选规则。

## 字段

`NodeMaintenance` 为 cluster scope，API 为 `maintenance.example.com/v1alpha1`，短名 `nm`。nodeName、mode、executionPolicy、jobPolicy、timeoutSeconds 和 drain 创建后不可变；paused、reason 可改；desiredState 只允许 Maintenance → Restored。

| 字段 | 默认 / 语义 |
| --- | --- |
| mode | Execute；PlanOnly 只产生一次快照报告，无 Lease、Node 写入或真实删除 |
| executionPolicy | Guarded；另一值 DrainCompatible |
| jobPolicy | Default 按策略解析为 Wait / Evict，并持久化生效值 |
| timeoutSeconds | 1800；0 不限时；从首次获槽起算，重启/暂停不重置 |
| ignoreDaemonSets | false；true 保留有效活动 DS |
| force | false；原生裸 Pod/孤儿规则，不绕过 PDB |
| deleteEmptyDirData | false；显式接受 emptyDir 内容随实例删除 |
| gracePeriodSeconds | -1 使用 Pod 终止宽限期；0 可立即终止 |
| disableEviction | false；仅兼容策略可显式启用独立授权的 Delete |
| skipWaitForDeleteTimeoutSeconds | 0；兼容策略可跳过旧 terminating 等待，本任务已发出的请求仍进入 pendingCleanup |
| podSelector | 空；兼容策略支持子集，子集完成不等于整个节点维护就绪 |
| chunkSize | 500；0 不分页，仅影响 List 请求方式 |
| dryRun | Client；PlanOnly 可选 Server，对删除选项携带 dryRun=All |

`disableEviction`、skip-wait 和非空 selector 在 Guarded 中由 CEL 拒绝。不可变规则避免任务中途改变承诺；需要改策略时先 Restored，再创建新任务。

API 发现确认不支持 Eviction 时，兼容策略仍需部署显式授权才可 Delete。429、403、超时和网络错误不会触发 Delete 回退。`PDBProtection` 条件明确标记路径。

## 结果阅读

DrainComplete 是选定目标处理结束；SourceDrained 是整个节点没有待处理的活动业务；WorkloadRecovered 是增强恢复判断，兼容模式保持 Unknown，无副本恢复目标时 Reason=NotApplicable；MaintenanceReady 是当前策略的整体许可。

Job Wait 记录本节点实例的 Running、Succeeded、Failed 或 GoneWithoutObservedResult。终态仍 terminating 时继续观察；退出不等 Job 完成。Job Evict 不承诺计算任务不重跑，也不承诺业务成功。

兼容不承诺相同驱逐顺序、并发度、速度或完全相同的保护门禁。SmartDrain 统一串行操作、持久化任务、使用 UID 前置条件，且预检拒绝时可能尚未 cordon；原生命令通常先 cordon。上述差异在实验中单独解释，不当作原生缺陷。

参考：[原生过滤器](https://github.com/kubernetes/kubectl/blob/v0.35.0/pkg/drain/filters.go)、[drain 实现](https://github.com/kubernetes/kubectl/blob/v0.35.0/pkg/drain/drain.go)、[Eviction API](https://kubernetes.io/docs/concepts/scheduling-eviction/api-eviction/)。
