# 运行与故障处理

下列命令均先设置专属 KUBECONFIG。运行示例的维护节点是 `smartdrain-dev-worker`，Operator 位于 worker3，NFS 服务运行在目标之外。不要对存在未退出任务的部署直接执行 `make undeploy`：先请求 Restored 并等 Completed，再卸载控制器和 CRD。

## 提交、暂停与退出

先用 `config/samples/plan.yaml` 查看一次预演，检查 status.pods、compute、storage 与 Conditions。正式执行选择 guarded.yaml 或 compatible.yaml。集群运维身份需要创建 cluster-scoped NodeMaintenance 的权限。

```powershell
$env:KUBECONFIG = "$PWD\.smartdrain.kubeconfig"
kubectl apply -f config/samples/guarded.yaml
kubectl get nm guarded-worker -o yaml
kubectl describe nm guarded-worker
kubectl -n smartdrain-system logs deploy/smartdrain-controller-manager --since=10m
```

暂停使用 pause-patch.json，恢复使用 resume-patch.json：

```powershell
kubectl patch nm guarded-worker --type merge --patch-file config/samples/pause-patch.json
kubectl patch nm guarded-worker --type merge --patch-file config/samples/resume-patch.json
kubectl patch nm guarded-worker --type merge --patch-file config/samples/restore-patch.json
```

暂停不撤销已经接受的驱逐；旧 UID 的观察继续。Restored 优先于暂停、超时、恢复门禁及 Job Wait。删除 CR 也触发相同清理流程，但保留 finalizer，直到清理完成。退出后不能将同一 CR 改回 Maintenance。

多节点维护为每节点创建一个 CR；全局串行槽在首个任务退出前不会释放。外部维护结束后恢复当前任务，下一项才能取得槽位。Lease acquireTime 是任务总期限起点；不会通过抢占过期 TTL 自动跳过阻塞任务。

`hack/new-maintenance.ps1` 支持节点名称列表或标签选择器，默认只输出 JSON；追加 `-Apply` 才提交。以下命令先生成指定节点的 PlanOnly 任务：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File hack/new-maintenance.ps1 -Selector 'smartdrain.dev/role=source' -PlanOnly
```

去掉 `-PlanOnly` 会生成实际维护任务；`-Policy DrainCompatible` 可显式选择兼容策略。提交前仍应检查目标节点与策略，脚本不绕过维护协议。

## 直接 Delete 的显式启用

默认 ServiceAccount 没有 `pods/delete`。只有确实要执行原生 `--disable-eviction` 对应行为时，安装可选权限，并修改 manager 参数：

```powershell
kubectl apply -f config/rbac/direct_delete.yaml
kubectl apply -k config/optional/direct-delete
kubectl -n smartdrain-system rollout status deployment/smartdrain-controller-manager
```

任务必须使用 DrainCompatible 且 drain.disableEviction=true。结果明确记录 PDBProtection=False。测试完成后恢复 `config/default`，确认 manager 参数恢复，并删除 `config/rbac/direct_delete.yaml` 中的可选角色和绑定。默认 429/PDB 拒绝不会调用这个路径。

## 常见阻塞

| 原因 | 检查与处理 |
| --- | --- |
| NativeFilter | 查看每个 Pod 的分类原因；不在运行中的不可变任务上强改选项，退出后重新提交 |
| ComputeFit | 查看 placements、requests、实际节点余量；Unknown 不等于调度无解 |
| RecoveryUnknown / StorageCheck | 识别当前增强模型边界；兼容策略保留通用驱逐能力，但不会承诺恢复 |
| WorkloadChanged | 检查扩缩容、滚动更新、同名控制器重建和模板变化；退出后用新基线重建任务 |
| SubmissionBackoff | 查看 currentOperation.lastReason 与 retryAfter；修复 PDB/权限/连接，不用 force 绕过预算 |
| ReplicaNotReady / ControllerNotAvailable | 查看替代 Pod 的事件、探针、调度与 Node 状态；退出可以解除本任务隔离帮助人工恢复 |
| TaskSlotBusy | 检查 Lease holderIdentity 对应的 CR UID；等待当前任务退出 |
| NodeNotReady | 先确认节点健康；显式 hold-cordon 可将节点继续交给人工维护 |

保留 cordon 后退出：在目标 Node 设置 `maintenance.example.com/hold-cordon=true`，然后请求 Restored。清理移除本任务 claim 并释放槽，但不解除节点隔离。原本已 cordon 的节点同样不会被本任务解封。

## 未知请求与清理阻塞

先保留证据，不要通过修改 status 或删除 finalizer 伪造成功：

1. 保存 CR 完整 YAML、task UID、Node UID/claim、Lease、currentOperation 和 pendingCleanup。
2. 按 namespace/name 读取 Pod，比较 UID；同名新 UID 表示旧实例已消失，不是新的重试目标。
3. 对旧 UID 仍存在的对象检查 deletionTimestamp、finalizers、kubelet 状态及 API 审计日志；`kubectl logs` 没有记录不证明请求未执行。
4. Accepted、AttemptStarted 或历史 Unknown 仍可能执行，Restored 继续观察，不会补发请求。
5. 如需人工结束旧实例，由管理员在确认具体 UID、数据责任和 finalizer 所属控制器后处置；一旦旧 UID 消失，正常清理可继续。不要强删 PVC 或盲目移除第三方 finalizer。

## 归属丢失与人工接管

ClaimOutcomeUnknown / OwnershipLost 表示当前证据不足以安全恢复原始调度状态。Node 同名新 UID 时控制器只结束对旧 Node 的责任，不修改新 Node。

人工接管须形成一条可追踪记录：操作者、时间、task/Node UID、原 claim、Lease、所有未决操作及接管结论。先请求 Restored，停止控制器并确认旧进程和相关操作已经被隔离；检查当前 Node 与未决旧 UID，避免多个维护流程同时处理同一节点。

若旧 claim 备份可验证，可由管理员在核对当前 Node UID 与 resourceVersion 后恢复原 claim，使正常退出协议接续。若原始状态不可证明，保留节点 cordon，并明确把节点交给人工维护；只在这一接管结论成立后，用匹配本任务/Node UID 的归属凭据与 hold-cordon 恢复清理路径。不得推测原始状态为可调度，也不得对新 UID 节点重建旧任务 claim。

Lease 持有者对应 CR 被外部强制删除时，不按时间自动清空。必须同样核对所有 Node claim、审计和未决请求，在停止旧控制器并完成接管后由管理员带 resourceVersion 修复。恢复 manager 后验证：claim 已移除、槽释放、节点调度状态符合接管决定、CR 清理结束。

## 指标与证据

```powershell
kubectl -n smartdrain-system port-forward deploy/smartdrain-controller-manager 8080:8080
```

访问 `/metrics` 查看 `smartdrain_pod_operations_total`、`smartdrain_pod_operation_duration_seconds`、`smartdrain_tasks` 和 `smartdrain_unresolved_operations`。请求计数包含重试，不等于恢复成功次数。状态日志保留 UID 关联；实验采样输出在 `artifacts/runs/`。
