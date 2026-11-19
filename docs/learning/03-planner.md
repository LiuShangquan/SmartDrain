# requests 容量预检

`internal/planner` 是纯计算模块，输入 Node、已绑定 Pod、迁移目标和卷候选，输出 Pass、Blocked 或 Unknown。它不调用 Binding API，不预留资源，也不重写 scheduler Score。

节点预算来自 allocatable 减去有效 requests。普通容器、init、原生 sidecar 与 overhead 的组合使用锁定版 component-helpers 的 PodRequests；不能简单累加所有 init，也不能重复加入 overhead。

即使两台节点各余 400m，总计 800m，也无法接纳一个需要 600m 的 Pod。逐个放置后必须从选中节点扣减虚拟预算，否则会把同一份余量重复分配。

找到完整布局是一份模型内见证。first-fit 没找到布局，并不证明所有排列都失败，因此一般返回 Unknown。只有明确的当前候选无法容纳单个目标等场景，才报告 Blocked。

未知资源口径、硬 Pod 亲和、已有 Pod 的反亲和或未知 scheduler profile 会降低结论可信度。兼容策略仍保留驱逐入口；增强策略不把未知当作安全。

预检和真实调度之间有时间窗口。竞争 Pod 可以消耗刚刚看到的余量；恢复门禁负责在替代 Pod Pending 后停止扩大影响。实验必须展示这个边界。
