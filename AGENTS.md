# Context Delegation Notice

The file "AI-CONTEXT.md" is the master source of truth for project context.
You must read and understand "AI-CONTEXT.md" before taking any action.
若与当前文件存在任何冲突，必须以 "AI-CONTEXT.md" 为准。

This file is only a compatibility pointer for tools expecting "AGENTS.md".
Do not maintain independent rules here unless explicitly required.

## TODO（用户 2026-08-04 要求记录，完成后删除本段）

P-013 视频超分代码已完成并通过两轮独立评审，但按定稿设计还有两步**必须真机完成**才能闭合验收（详见 `docs/loopx/plans/2026-08-04-feature-expansion.md` 交接记录）：

1. **构建并打包 sidecar 运行时**：在 Apple Silicon 上运行 `bash scripts/build_enhance_runtime.sh`（需要 Xcode CLT + cmake），把产出的 `enhance-runtime/` 目录放入应用包 `Contents/Resources/` 并纳入签名。在此之前应用内超分入口显示"运行时未打包"，属设计内的不可用状态。
2. **M4 真机样片验收**：一般/动漫各一部真实样片跑完整超分闭环，覆盖取消、磁盘不足、崩溃续跑场景；不可用单元测试替代。

另有一项用户已裁决后补：洞察页 EXPLAIN 证据，在能连库的终端运行
`go run ./cmd/stats_explain > docs/loopx/design/2026-08-04-insights-explain.md`。

Generated at: 20260224110626


<claude-mem-context>
# Memory Context

# [CineInsight] recent context, 2026-04-30 7:28pm GMT+8

No previous sessions found.
</claude-mem-context>