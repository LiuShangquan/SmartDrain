# SmartDrain 学习路线

学习文档与实现模块对应。建议同时打开 `api/`、`internal/` 和测试，沿一次正常维护与一次失败恢复阅读。

| 文档 | 代码位置 | 核心问题 |
| --- | --- | --- |
| [API 与契约](01-api.md) | `api/v1alpha1` | spec 与 status 为什么分离，哪些字段能修改 |
| [工作负载](02-workload.md) | `internal/workload` | 为什么 owner UID、Pod Ready 和 Job 完成是不同事实 |
| [容量模型](03-planner.md) | `internal/planner` | requests 为什么不能直接换成实时 CPU，为什么贪心失败不等于无解 |
| [维护协议](04-maintenance.md) | `internal/maintenance` | 请求成功但响应丢失后，如何避免操作错误对象 |
| [控制器](05-controller.md) | `internal/controller` | Reconcile 为什么每次只推进一小步 |
| [可观测性](06-observability.md) | `internal/observability` | 请求计数与成功迁移为什么不同 |
| [实验](07-tests.md) | `test`、`docs/experiments.md` | 单测、真实 API 和真实集群各能证明什么 |

Go 代码使用小包、显式依赖和窄接口。没有通用工作流引擎或自定义插件注册平台；未知控制器保留通用驱逐能力，增强恢复检查明确报告 Unknown。

模块路径暂用 `github.com/smartdrain/smartdrain` 作为本地项目命名；它不表示已创建或拥有同名远程仓库。公开发布时按实际仓库地址统一替换。
