# 测试分别证明什么

## 纯模块测试

`internal/workload` 直接调用固定版本 kubectl 过滤器进行 144 个组合的对照。它能发现筛选顺序差异，不能证明 kubelet 真正终止容器。容量测试覆盖有效 requests、跨 namespace 扣减、槽位、碎片、虚拟扣减、存储候选交集和相关未知约束。

`internal/controller` 使用 fake client 验证协议顺序。写入故障测试在 14 个持久化位置“写成功后返回 EOF”，重建 reconciler 并检查正常收敛、Node 标签保留和槽释放。纯测试中的 Pod 状态是夹具，不作为真实恢复证据。

## 真实 API

`test/integration` 针对独立 kind API Server 校验 CRD 默认、CEL 不可变规则、不可逆退出、status 乐观并发。实际 API 能执行准入和资源版本冲突；普通 fake client 不能替代它。这里使用真实 API，未把 envtest 当作已执行的附加证据。

## 真实控制器与调度

`test/e2e` 创建实际 Deployment、RS、RC、StatefulSet、Job、PDB、PVC；由 scheduler 绑定，kubelet 更新状态。测试应用在真实容器中延迟 Ready、处理 SIGTERM、完成 Job、写入并读回 NFS 文件。未通过写 Pod.status 冒充调度或恢复。

NFS 测试同时检查旧/新 Pod UID、Node 变化及文件 token；local PVC 测试保留实际 Pending，说明“驱逐完成”和“存储恢复”不是一回事。真实静态 Pod 通过 kubelet manifest 路径创建，不能用手写 mirror 注解冒充。

响应丢失测试先缩容独立实验集群的 manager，再使用实际 ServiceAccount 调用 Eviction。API 真正接受后，测试 Executor 丢弃响应并返回 EOF；重建 reconciler，核对退出不补发删除，旧 UID 未消失时不解除隔离。故障注入适配器只在测试代码中存在。

## 公平对照

对照固定 kubectl 版本、初始分布、探针、请求量和 PDB。外部采样同时记录源节点排空、健康副本最低点、共同的 10 秒恢复窗口及 HTTP 探测失败数；严格 PDB 的未完成不计作成功。

先运行一个场景检查夹具，再重复三次。不要把 Guarded 的更长恢复等待写成“性能退化”，也不要把无 PDB 场景中原生同时驱逐的行为写成原生 drain 不支持 PDB。kind 节点共享宿主机；小样本只能支撑这些具体场景的行为结论。

## 建议练习

从 `TestFragmentedCapacityBeforeEviction` 追踪 600m 目标与两个 400m 候选；说明为什么合计 800m 不等于单 Pod 能放下。再从响应丢失案例解释为什么 2xx、旧 UID 消失和替代 Pod Ready 必须分别观察。修改一个模板声明的环境变量，检查增强恢复为何停止，以及 Restored 为何仍能退出。
