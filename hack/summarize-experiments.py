"""汇总全部原始对照结果，保留未完成实验，不选择最好的一次。"""
import csv
import json
import statistics
from collections import defaultdict
from pathlib import Path

root = Path(__file__).resolve().parent.parent
source = root / "artifacts/runs/comparison"
target = root / "artifacts/results"
target.mkdir(parents=True, exist_ok=True)
rows = []
groups = defaultdict(list)
for path in sorted(source.glob("*.json")):
    result = json.loads(path.read_text(encoding="utf-8"))
    if "Policy" not in result:
        continue
    failure_start = None
    longest = 0.0
    for sample in result["Samples"]:
        at = sample["seconds"]
        if not sample["HTTPReady"] and failure_start is None:
            failure_start = at
        elif sample["HTTPReady"] and failure_start is not None:
            longest = max(longest, at - failure_start)
            failure_start = None
    if failure_start is not None:
        longest = max(longest, result["Samples"][-1]["seconds"] - failure_start)
    row = {key: value for key, value in result.items() if key != "Samples"}
    row["LongestObservedHTTPFailureSeconds"] = round(longest, 3)
    rows.append(row)
    groups[result["PDB"], result["Policy"]].append(row)
if not rows:
    raise SystemExit("No comparison records found")
with (target / "comparison-trials.csv").open("w", newline="", encoding="utf-8") as out:
    writer = csv.DictWriter(out, fieldnames=list(rows[0]))
    writer.writeheader()
    writer.writerows(rows)

lines = ["# 实际对照结果", "", "全部原始试验均计入；时间仅对完成者取中位数，未完成单列。HTTP 为 API Server Service proxy 稀疏探测，不是压测。", "",
         "| PDB | 路径 | 完成/总数 | 源节点排空中位数 | 共同恢复中位数 | 健康副本最低点 | HTTP 失败/探测 |",
         "| --- | --- | --- | --- | --- | --- | --- |"]
for pdb in ("none", "permissive", "strict"):
    for policy in ("kubectl", "DrainCompatible", "Guarded"):
        trials = groups[pdb, policy]
        complete = [r for r in trials if r["Outcome"] == "Recovered"]
        timing = lambda key: f"{statistics.median(r[key] for r in complete):.2f}s" if complete else "未完成"
        minimum = min(r["MinimumReady"] for r in trials) if trials else "—"
        failed = sum(r["FailedRequests"] for r in trials)
        requests = sum(r["Requests"] for r in trials)
        lines.append(f"| {pdb} | {policy} | {len(complete)}/{len(trials)} | {timing('SourceDrainedSeconds')} | {timing('RecoveredSeconds')} | {minimum} | {failed}/{requests} |")
lines += ["", "完整逐次数据见 comparison-trials.csv；原始采样在 artifacts/runs/comparison。共同恢复终点要求源节点排空、两个副本 Ready 且 HTTP 连续成功 10 秒。严格 PDB 不允许中断，排空时间记录为未完成。", ""]
(target / "comparison-summary.md").write_text("\n".join(lines), encoding="utf-8")
print(f"Summarized {len(rows)} trials into {target}")
