# 实验与复现

所有操作面向专属 `smartdrain-dev` kind 集群。测试会暂时 cordon 接收节点制造初始分布，扫描目标节点的全部 namespace；不要同时运行手工演示和自动测试，也不要把测试指向有其他业务的集群。夹具验证 providerID 后才执行。

## 环境

- Kubernetes/kubectl v1.35.0；kind node digest 见 `versions.env`。
- Go 最低 1.25，开发构建使用 Windows Go 1.26.5，Linux race 使用 WSL Go 1.26.8。
- controller-runtime v0.23.0，k8s.io 模块 v0.35.0，controller-gen v0.20.0。
- 一个 control-plane、三个 worker；使用默认 scheduler 和 feature gates。Operator 放在 worker3，源节点为 worker。
- 容量用实际 Node allocatable 和实际绑定 Pod requests；kind 节点共享宿主机，不用于推导物理集群吞吐量。

先执行 README 的部署脚本。Windows 官方 kubectl 可下载并验证：

```powershell
Invoke-WebRequest https://dl.k8s.io/release/v1.35.0/bin/windows/amd64/kubectl.exe -OutFile bin/kubectl-v1.35.0.exe
Invoke-WebRequest https://dl.k8s.io/release/v1.35.0/bin/windows/amd64/kubectl.exe.sha256 -OutFile bin/kubectl-v1.35.0.exe.sha256
$expected=(Get-Content bin/kubectl-v1.35.0.exe.sha256 -Raw).Trim()
if ((Get-FileHash bin/kubectl-v1.35.0.exe -Algorithm SHA256).Hash -ne $expected) { throw 'Checksum mismatch' }
$env:KUBECONFIG="$PWD\.smartdrain.kubeconfig"
$env:SMARTDRAIN_KUBECTL="$PWD\bin\kubectl-v1.35.0.exe"
```

## 手工演示

```powershell
kubectl cordon smartdrain-dev-worker2 smartdrain-dev-worker3
kubectl apply -f test/fixtures/demo.yaml
kubectl -n smartdrain-demo rollout status deployment/web --timeout=120s
kubectl uncordon smartdrain-dev-worker2 smartdrain-dev-worker3
kubectl -n smartdrain-demo get pods -o wide
kubectl apply -f config/samples/plan.yaml
kubectl get nm plan-worker -o yaml
kubectl apply -f config/samples/guarded.yaml
kubectl get nm guarded-worker -w
```

观察每次旧 UID 消失后，替代 Pod 延迟 Ready，再经过健康窗口才驱逐下一个。演示结束请求 Restored，等 Completed 后删除 CR 与 `smartdrain-demo` namespace。不要删除任务 finalizer 跳过清理。

## 分层测试

```powershell
go test -count=1 ./...
go vet ./...
$env:SMARTDRAIN_API_TESTS='1'
go test -v -count=1 ./test/integration
$env:SMARTDRAIN_E2E='1'
go test -v -count=1 -timeout=20m ./test/e2e
```

普通包测试不连接集群。真实 API 测试验证默认值、CEL、status 冲突、同名新 UID 保护及默认 ServiceAccount 没有 Pod Delete、业务写入和 PVC 删除权限。默认 E2E 覆盖副本控制器、Job、PDB、子集、skip-wait、dry-run 和 local PVC。

需要独立启用的用例：

```powershell
$env:SMARTDRAIN_FAULT_TESTS='1'
go test -v -count=3 -timeout=10m ./test/e2e -run TestAcceptedResponseLostAcrossRestartAndRestore
$env:SMARTDRAIN_MIXED_TESTS='1'
go test -v -count=1 -timeout=5m ./test/e2e -run TestRealStaticPodCronJobAndUnknownOwner
$env:SMARTDRAIN_DIRECT_DELETE_TESTS='1'
go test -v -count=1 -timeout=5m ./test/e2e -run TestExplicitDirectDeleteRequiresDeploymentOptIn
```

直接 Delete 测试先验证默认部署拒绝，再安装独立 RBAC 和开关，确认显式 Delete 会绕过严格 PDB且标记 PDBProtection=False，最后恢复默认部署与权限。不是普通驱逐失败时的自动回退。

## 真实 NFS 读回

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File hack/install-nfs-dev.ps1
$env:SMARTDRAIN_NFS_SERVER=(docker inspect smartdrain-nfs --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}').Trim()
go test -v -count=3 -timeout=15m ./test/e2e -run TestNFSCrossNodeDataReadback
```

固定 NFS CSI v4.12.1 的 amd64 镜像，精确摘要在 `test/fixtures/nfs/csi-nfs-node.yaml`。服务端容器在 kind 网络中提供 NFSv4，使用独立 Docker volume；PV 通过真正的 NFS CSI 挂载，并未给每个节点创建同路径 hostPath 冒充共享存储。静态 PV 场景无需动态 provisioner。

测试记录迁移前后的 Pod UID、Node、PVC/PV 和文件 token。Deployment 与 StatefulSet 都必须移动到其他节点并读回同一 token。退出后保留的 NFS 测试数据位于专属 volume，不代表生产数据耐久性或数据库一致性验证。

本机处理过两种环境问题：kind 节点继承了不可达的本机代理；多平台镜像索引只下载了 amd64 内容。安装脚本通过 Docker 侧拉取固定摘要、导出明确平台、导入各 kind 节点并补充 digest 引用解决，未修改用户全局代理。

## 资源碎片与恢复对照

```powershell
$env:SMARTDRAIN_EXPERIMENTS='1'
go test -v -count=1 -timeout=15m ./test/e2e -run TestFragmentedCapacityBeforeEviction
$env:SMARTDRAIN_COMPARISON='1'
go test -v -count=1 -timeout=30m ./test/e2e -run TestRepeatedDrainComparison
python hack/summarize-experiments.py
```

碎片实验内部对三条路径各重复三轮。真实占位 Pod 由 scheduler 绑定，使两个候选各余 400m；目标需要 600m。Guarded 应在 cordon/驱逐前阻塞，兼容模式和原生 drain 则执行驱逐，替代 Pod 因容量停在 Pending。

恢复对照内部共 27 组：三条路径 × 无/宽松/严格 PDB × 三轮。每组两个副本、相同初始分布、100m/32Mi requests、4 秒就绪延迟。宽松 PDB 为 minAvailable=1，严格 PDB 为 minAvailable=2。普通场景操作期限 60 秒，严格 PDB 为 25 秒；严格场景采样到 32 秒并记未完成。

共同终点是源节点无活动业务、两个健康副本和 HTTP 连续成功 10 秒。每约 500ms 通过 API Server Service proxy 探测 `/ready`，单请求 800ms 超时，记录所有采样。它是稀疏可用性观察，不是固定吞吐量压测；不直接推导用户请求失败率。

结果保留排空时间、共同恢复时间、健康副本最低点、HTTP 失败次数/探测次数和最长观察失败窗口。全部试验写入 CSV，未完成单列，不挑选最好一次。实际汇总见 [对照结果](../artifacts/results/comparison-summary.md)。

这 27 组结果绑定 `artifacts/results/comparison-environment.json` 中记录的镜像，工作区保存了对应的 `artifacts/runs/comparison-images.tar`。对照完成后补充了诊断信息和边界状态修正；最终部署另做完整功能回归，不把旧镜像的时间数据当成最终镜像的重新测量。

## 证据目录与边界

`artifacts/runs/` 保存原始日志、Pod/Node/CR/PVC/PV/Events JSON 和 HTTP 时间序列，体积较大因此不进 Git；本次工作区保留这些文件。`artifacts/results/` 保存可提交的逐次结果与汇总。测试结束会请求正常退出，等待测试 namespace 清理完再进入下一案例，避免异步 namespace 删除污染后续整节点测试。

测试矩阵与最终通过情况见 [实现与验证记录](progress.md)。远程 CI 尚未执行；envtest 未额外运行，本次 API 验证使用真实 kind API Server。没有生产环境测试、数据库一致性验证或其他 CSI 后端恢复承诺。
