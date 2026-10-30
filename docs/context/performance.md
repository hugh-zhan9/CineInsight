# 作品集进度性能基线

2026-10-30，Apple M5 / darwin arm64，Go 测试 CPU=2。合成 SQLite，每个作品集 20 个成员，一个已看、其余未看；实际调用 ListCollectionProgressPage，第一页 12 项。设置不计入计时，每个规模 3 次查询。这是小样本工程基线，不是实际用户库 SLA 或峰值内存测量。

| 作品集 / 成员 | 原耗时 | 优化后耗时 | 原累计分配 / 次 | 优化后累计分配 / 次 |
|---|---:|---:|---:|---:|
| 100 / 2,000 | 4.49 ms | 4.12 ms | 2.76 MB | 1.65 MB |
| 1,000 / 20,000 | 41.76 ms | 40.01 ms | 31.93 MB | 20.87 MB |
| 5,000 / 100,000 | 210.64 ms | 199.35 ms | 164.32 MB | 109.01 MB |

最后一组累计分配降低约 34%。修改仅复用按作品集排序的结果切片，单成员作品不排序；追加版本成员时限制切片容量以免覆盖下一部。查询仍随候选成员总数增长，未增加缓存、汇总表或改变分页/版本口径。后续若真实库数据表明仍慢，再单独评估数据库聚合及兼容性。

复现：在仓库根执行 `/Users/zhangyukun/.local/bin/agent-go test ./services -run '^$' -bench '^BenchmarkCollectionProgressPage$' -benchtime=3x -count=1`。benchmark 自建临时 SQLite，不使用用户库；生产 PostgreSQL 性能不由此证明。
