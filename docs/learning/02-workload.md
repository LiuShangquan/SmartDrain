# 工作负载分类与恢复

`classify.go` 先按锁定版 kubectl 的过滤顺序判断通用动作。恢复分析器认识哪些 Kind，不决定原生驱逐资格。例如未知自定义 controller owner 可以走兼容驱逐，而恢复检查只能报告未知。

DaemonSet、mirror Pod、emptyDir 和裸 Pod 各有不同语义。`force` 只解除原生相应筛选限制，不会绕过 PDB；`deleteEmptyDirData` 表示接受本地临时数据清理；真正的镜像 Pod 来自 kubelet，实验不能用普通 Pod 加注解冒充。

`owner.go` 沿 Pod → ReplicaSet → Deployment 的 controller 引用追踪 UID。Deployment 的 Pod 直接 owner 是 RS，不是 Deployment。已有父引用但父对象缺失，不能把 RS 降级成独立 RS，否则会改变任务承诺。

Deployment/RS/RC 的恢复看整体健康容量，不强行把旧 Pod 与某个新 Pod 配对。StatefulSet 需要同 ordinal 的新 UID、相同 revision 和卷身份。Job 自然等待只看源节点实例是否结束，不能拿 Job 的 parallelism 当 Ready 副本目标。

`storage.go` 将驱逐成功与卷可迁移分开。PVC/PV 的名字可重复，绑定必须双向核实 UID；CSI 后端摘要可以发现同名配置被替换。NFS profile 的配置检查不等于提前完成挂载，最终仍需要真实跨节点读回。

阅读练习：让新实例 Pending，确认旧 Pod 消失为什么还不足以通过 Guarded；再比较 DrainCompatible 下的 SourceDrained 与 WorkloadRecovered。
