# 最终验收记录

日期：2026-11-19。环境为 Windows PowerShell + Docker Desktop + 专属 kind 集群，Linux race 在 WSL Ubuntu 运行。完整矩阵见 [实现与验证记录](../../docs/progress.md)，复现入口见 [实验手册](../../docs/experiments.md)。

| 检查 | 结果 | 工作区原始日志 |
| --- | --- | --- |
| Go 全包测试、vet、格式 | 通过；普通运行不启用集群测试 | `artifacts/runs/unit-final.txt`、`vet-final.txt`、`generation-final.txt` |
| Linux race | controller、maintenance、planner、workload 通过，无竞争报告 | `artifacts/runs/race-linux-final.txt` |
| 生成文件与部署清单 | deepcopy/CRD/RBAC 可重生成；服务端校验通过 | `artifacts/runs/generation-final.txt`、`manifests-final.txt` |
| 真实 API 与示例 | 契约、显式零值、UID、默认权限、三份 kubectl 示例通过 | `artifacts/runs/integration-final.txt` |
| 批量创建入口 | PowerShell 生成的 PlanOnly JSON 通过服务端预演，未创建实际任务 | `artifacts/runs/entrypoint-final.txt` |
| 完整 E2E | 14 项顶层用例通过，460.596 秒；对照与碎片用例由独立实验运行 | `artifacts/runs/e2e-final.txt` |
| 扩展混合节点 | 通过，69.982 秒；Deployment、独立 RS、RC、StatefulSet、CronJob、未知 owner 与真实静态 Pod 同节点 | `artifacts/runs/e2e-mixed-final.txt` |
| 故障与 NFS 重复 | 最终部署后各补跑两次通过，179.335 秒；连同完整回归各三次。NFS 每次都覆盖 Deployment 与 StatefulSet | `artifacts/runs/e2e-repeats-final.txt` |
| 开发部署与 NFS 安装脚本 | Windows PowerShell 实际执行通过，重部署及重复安装可运行 | `artifacts/runs/deploy-final.txt`、`nfs-setup-final.txt` |
| 指标端点 | 最终 manager 的真实 `/metrics` 可读取，状态收集成功值为 1 | `artifacts/runs/metrics-final.prom` |
| 原生 drain 对照 | 27 组断言通过：18 组恢复、9 组严格 PDB 未完成 | `artifacts/runs/e2e-comparison.txt` |
| 资源碎片 | 三条路径各三轮通过，另验证真实 local PVC 的恢复未知 | `artifacts/runs/e2e-capacity-local.txt` |

完整 E2E 包含显式 Delete 开关、响应丢失、Job 失败/超时、Deployment、PDB、Job 退出、本地 PVC、静态 Pod/CronJob/未知 owner、原生选项、skip-wait 清理责任、NFS、RS/RC/StatefulSet、Job 自然完成和服务端 dry-run。

27 组时间数据对应 [对照环境](comparison-environment.json) 中的镜像。最终 CRD 修正了可选默认字段的客户端校验，并重新部署；功能测试和对照耗时不混作同一构建的测量。

最终构建的版本、镜像和二进制摘要见 [构建记录](final-build.json)；实现、测试夹具和配置共 84 个文件的摘要见 [源码清单](source-sha256.txt)。原始日志中 controller-runtime 的未初始化测试日志器提示来自进程内故障夹具，未导致测试失败；实际部署的 manager 正常配置了结构化日志。

[集群收尾检查](final-state.json) 已通过：四个节点 Ready 且没有遗留 cordon/claim；无维护任务、任务槽占用、测试 namespace 或测试 PV；静态 Pod manifest 已移除。manager 恢复为一个副本，默认参数不启用直接 Delete，默认账号无 Pod Delete 权限，可选绑定已删除。

专属 kind 集群、默认 Operator 与 NFS 服务保留，方便继续开发和手工演示。NFS 专属 Docker volume 中的测试 token 文件仍保留。所有集群命令继续使用项目 `.smartdrain.kubeconfig`。

远程 GitHub Actions、envtest、生产集群和数据库一致性未执行；没有将这些项目计为通过。
