# SmartDrain

基于 Go、controller-runtime 和 Kubernetes Eviction API 的节点维护 Operator。保留原生 drain 的对象覆盖与显式选项，在同一执行协议上提供容量预检、工作负载恢复检查和中断恢复。

| 策略 | 行为 |
| --- | --- |
| `Guarded`（默认） | 驱逐前检查资源、存储与工作负载基线；逐个迁移并等待健康窗口；Job 默认自然等待 |
| `DrainCompatible` | 按原生规则处理所有目标，容量及恢复未知不会缩减驱逐范围；Job 默认驱逐；仍保留 UID、任务互斥与清理协议 |

Deployment、独立 RS、RC、StatefulSet、Job/CronJob、未知 controller owner、裸 Pod、DaemonSet、镜像 Pod 和终态 Pod 都进入统一分类。`force` 不绕过 PDB。直接 Delete 必须同时启用 CR 选项、部署开关和独立 RBAC。

## 开发环境启动（已安装 Go、Docker、kind 和 kubectl）

首次下载镜像、网络受限或首次编译会增加时间。开发示例固定在 **amd64、Kubernetes v1.35.0、一个控制面和三个 worker**。所有命令使用项目专属 kubeconfig。

Windows PowerShell：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File hack/deploy-dev.ps1 -CreateCluster
$env:KUBECONFIG = "$PWD\.smartdrain.kubeconfig"
kubectl apply -f config/samples/guarded.yaml
kubectl get nodemaintenances -w
```

Linux：

```bash
bash hack/deploy-dev.sh --create-cluster
export KUBECONFIG="$PWD/.smartdrain.kubeconfig"
kubectl apply -f config/samples/guarded.yaml
kubectl get nodemaintenances -w
```

已有 `smartdrain-dev` 集群时，省略创建集群选项即可重新构建部署。实际业务演示与自动造负载验证见 [实验手册](docs/experiments.md)。在其他集群部署时，先把目标外的 Operator 节点标记为 `smartdrain.dev/role=operator`，按实际镜像仓库修改 `config/manager/manager.yaml`；示例源节点名称也需替换。

只有 `MaintenanceReady=True` 才表示满足当前策略的维护条件。进入 Maintenance 后，节点保持 cordon，任务持续占用全局槽；外部维护结束后显式退出：

```powershell
kubectl patch nodemaintenance guarded-worker --type merge --patch-file config/samples/restore-patch.json
kubectl wait nodemaintenance/guarded-worker --for=jsonpath='{.status.phase}'=Completed --timeout=120s
```

Completed 表示退出清理已完成。清理仍被旧 UID 或未知请求阻塞时，应按 [运行手册](docs/runbook.md) 排查。

## 开发与验证

```powershell
go test -count=1 ./...
go vet ./...
powershell -NoProfile -ExecutionPolicy Bypass -File hack/test.ps1 -Cluster
```

普通 `go test ./...` 运行单元测试并跳过需要显式环境变量的真实集群测试。API、故障、NFS 和对照实验的启用方式及实际结果见 [实验手册](docs/experiments.md) 与 [验证记录](docs/progress.md)。CI 配置在 `.github/workflows/ci.yaml`；本地通过不代表远程 CI 已运行。

## 阅读入口

- [按模块学习](docs/learning/00-reading-guide.md)：API → 分类/恢复 → 容量 → 协议 → 控制器 → 观测 → 实验。
- [设计与关键不变量](docs/design.md)：UID、持久日志、Node 归属、清理顺序。
- [API 和兼容矩阵](docs/compatibility.md)：支持对象、原生选项和增强模型边界。
- [运行与故障处理](docs/runbook.md)：暂停、退出、直接 Delete、未知请求与人工接管。
- [实验与原始证据](docs/experiments.md)：固定版本、共同终点、重复结果和复现命令。
- [项目经历与面试](docs/project-experience.md)：可以使用的描述、实际证据与需要讲清楚的取舍。

目录遵循 Kubernetes Controller 项目的组织：`api/v1alpha1` 定义 API，`internal` 保存业务实现，`cmd` 装配 manager，`config` 保存生成的 CRD/RBAC 与部署，`test` 保存真实夹具和分层测试，`hack` 保存开发脚本。

容量 Pass 是当前快照下的可解释布局，不是调度预留。Pod Ready 与文件读回也不证明数据库复制或业务一致性。项目提供个人实验环境的证据，不将 kind 结果写成生产集群性能提升。
