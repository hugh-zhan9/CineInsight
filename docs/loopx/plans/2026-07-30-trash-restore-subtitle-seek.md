# 回收站恢复与字幕命中跳转

## Source And Goal

- Source: 用户于 2026-07-30 批准实现两个 P0：回收站/撤销删除/恢复，以及字幕搜索直接跳到命中时间。
- Goal: 新发生的记录删除和文件删除均可在应用内查看并恢复；字幕搜索结果可复用虚拟列表，并在打开内嵌预览时跳到首个命中片段时间。

## Boundaries And Global Constraints

- 保持现有 `DeleteVideo`、`BatchDeleteVideos` 和字幕搜索 API 的公开签名与既有调用行为兼容。
- 恢复默认使用删除前原路径；目标已存在时失败并保留回收站条目，绝不覆盖文件。
- 仅为本功能上线后的删除创建可恢复条目；不猜测或回填历史软删除记录。
- 仅恢复视频记录、原文件位置和现有同名字幕索引；不新增永久清空、自动过期或跨目录恢复。
- 字幕跳转只作用于支持内嵌预览的格式；外部播放器预览保持统计中立且不承诺时间跳转。
- 不改变 AI 标签、同源关系、播放统计和标签关联的既有产品语义。

## Execution Slices

### P-001: 可恢复删除领域闭环

- Outcome: 删除视频时原子记录删除快照和可选回收站路径；可按时间倒序列出条目并安全恢复到原路径，文件或数据库恢复失败时保留可重试状态。
- Depends on: none
- Write scope: `models/`, `database/`, `services/video_service.go`, `services/trash_service.go`, `services/*_test.go`, `app.go`
- Source anchors: 回收站/撤销删除/恢复；原路径冲突时不覆盖；旧删除记录不回填；公开删除 API 兼容。
- Acceptance: 仅删记录和同时删文件都生成条目；恢复后视频重新出现在活动查询中；文件回到原路径；同名字幕重新索引；目标冲突和源文件缺失均不丢失条目。
- Verification: `go test ./services -run 'Trash|Restore|DeleteVideo'`; `go test ./...`; `go vet ./...`
- Review focus: 文件系统与数据库的失败补偿、软删除唯一索引、恢复时的路径冲突和字幕索引一致性。

### P-002: 回收站与撤销 UI

- Outcome: 视频列表提供回收站入口、条目列表和恢复动作；单个删除、批量删除与清理候选删除后都出现可操作的撤销提示。
- Depends on: P-001
- Write scope: `frontend/src/components/`, `frontend/scripts/`, `frontend/package.json`, generated Wails bindings
- Source anchors: 应用内查看并恢复；删除后可撤销；不增加永久清空。
- Acceptance: 用户能打开回收站、识别“仅删记录/文件已移入回收站”、恢复条目并刷新当前视图；删除成功后出现撤销入口；失败显示明确错误且条目仍在。
- Verification: `npm run test:trash-restore`; `npm test`; `npm run build`
- Review focus: 批量删除部分失败、清理候选删除、重复点击恢复和弹窗关闭后的状态同步。

### P-003: 字幕命中时间跳转与虚拟列表

- Outcome: 字幕搜索结果携带首个命中时间，点击结果的预览动作后内嵌播放器跳转到该时间；字幕结果使用现有虚拟列表壳。
- Depends on: P-002
- Write scope: `frontend/src/components/PreviewDrawer.vue`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/VideoListRow.vue`, `frontend/src/utils/`, `frontend/scripts/`
- Source anchors: 字幕搜索直接跳到命中时间；字幕搜索接入现有虚拟列表；外部预览不承诺跳转。
- Acceptance: 命中行展示可读时间；打开预览后在 metadata 就绪时 seek 到 `start_time_ms`；切换视频或普通文件搜索不会复用旧时间；字幕结果滚动使用虚拟列表并保持加载完成语义。
- Verification: `npm run test:subtitle-seek`; `npm run test:virtual-list`; `npm test`; `npm run build`
- Review focus: 0 毫秒、无命中时间、切换预览请求、metadata 到达顺序和虚拟行高度缓存。

## Integration And Final Verification

- 运行 `gofmt` 覆盖所有编辑的 Go 文件并执行 `go test ./...`、`go test -race ./services`、`go vet ./...`。
- 执行 `npm test` 与 `npm run build`，确认统一测试入口覆盖原有 8 个脚本及两个新增行为测试。
- 运行 `git diff --check`，检查 Wails 绑定与公开方法一致，并更新 `AI-CONTEXT.md` 描述新能力。

## Handoff And Residual Risks

- Status: completed
- Blockers: none
- Review resolution: 文件移动改为硬链接或 `O_EXCL` 排他复制；删除和恢复使用持久状态并在启动时对账；数据库事务错误先确认提交终态再决定补偿；强文件身份和 SHA-256 阻止弱指纹误移动；复制回退校验复制期间源文件未变化；异常条目和最近错误可在回收站内诊断与恢复。
- Residual risks: 外部播放器无法可靠 seek；历史软删除没有回收站路径，因此不会出现在新回收站中；前端复杂竞态目前仍由源码契约测试覆盖，后续可补 Vue 组件级异步测试。
- Resume note: 三个执行切片及两轮独立审查修复均已完成；若继续增强，优先增加 Vue 组件级并发交互测试。

## Execution rules for the consuming agent

- Execute slices in dependency order; verify each slice with its exact commands before starting dependents.
- Two slices may run in parallel only when neither depends on the other and their write scopes are disjoint; integrate results sequentially.
- Follow the installed working agreement for verification, review, stop, and Git discipline throughout.
