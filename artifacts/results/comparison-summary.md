# 实际对照结果

全部原始试验均计入；时间仅对完成者取中位数，未完成单列。HTTP 为 API Server Service proxy 稀疏探测，不是压测。

| PDB | 路径 | 完成/总数 | 源节点排空中位数 | 共同恢复中位数 | 健康副本最低点 | HTTP 失败/探测 |
| --- | --- | --- | --- | --- | --- | --- |
| none | kubectl | 3/3 | 0.51s | 15.76s | 0 | 28/96 |
| none | DrainCompatible | 3/3 | 1.59s | 16.27s | 0 | 25/99 |
| none | Guarded | 3/3 | 16.35s | 32.08s | 1 | 0/192 |
| permissive | kubectl | 3/3 | 6.09s | 21.29s | 1 | 0/129 |
| permissive | DrainCompatible | 3/3 | 8.11s | 23.34s | 1 | 0/139 |
| permissive | Guarded | 3/3 | 16.78s | 32.07s | 1 | 0/192 |
| strict | kubectl | 0/3 | 未完成 | 未完成 | 2 | 0/192 |
| strict | DrainCompatible | 0/3 | 未完成 | 未完成 | 2 | 0/192 |
| strict | Guarded | 0/3 | 未完成 | 未完成 | 2 | 0/192 |

完整逐次数据见 comparison-trials.csv；原始采样在 artifacts/runs/comparison。共同恢复终点要求源节点排空、两个副本 Ready 且 HTTP 连续成功 10 秒。严格 PDB 不允许中断，排空时间记录为未完成。
