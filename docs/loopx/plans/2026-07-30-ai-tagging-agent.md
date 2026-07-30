# 受限 AI 打标 Agent

## Source And Goal

- Source: `docs/loopx/design/2026-07-30-ai-tagging-agent/需求设计文档.md`
- Goal: 把现有固定 AI 打标流水线升级为最多四轮的受限 Agent：模型可在服务端预算内决定补帧数量、请求本地临时转写和查询同源内容视频；同源关系可见、持久、可否认；最终标签继续走闭集待审和人工批准。

## Boundaries And Global Constraints

- 不创建或修改正式 `.srt`，不翻译，不自动安装字幕运行时或模型。
- 不建设通用语义相似/向量检索，不直接复制同源视频标签，不上传原始音频。
- 外部仅发送既有视频元数据/JPEG、临时转写文本和受限候选 JPEG；日志/数据库不保存图片、音频或临时转写全文。
- 保持现有 Wails 方法、候选状态、标签闭集、人工审批、自动标签例外和后台串行 Worker 行为兼容；新接口/字段只做加法。
- 保留工作区中用户现有的标签合并、自动短视频标签和文件迁移改动；重叠文件必须基于当前工作树顺序集成。
- 实施期间保留既有文件迁移、自动短视频标签和标签合并改动；最终工作树已可通过全量 Go 测试。

## Execution Slices

### P-001: 固定 Agent 数据、配置和纯算法基础

- Status: completed

- Outcome: 新设置、Agent step、视觉指纹和同源关系模型可增量迁移；补帧位置规划、规范视频对、内容指纹、感知哈希和候选评分均为有边界的纯函数并有回归测试。
- Depends on: none
- Write scope: `models/ai_tagging.go`, `models/video.go`, `models/schema.go`, `database/database.go`, `services/ai_tagging_config.go`, `services/settings_service.go`, new `services/ai_same_source*.go`, corresponding `*_test.go`
- Source anchors: AC-003, AC-004, AC-010, AC-011, AC-012, AC-016; D-004, D-006, D-007, D-009, D-010, D-011; TC-002, TC-003, TC-007, TC-008
- Acceptance: 默认额外帧上限 20 且归一化到 1–100；新表/索引重复迁移安全；视频对唯一且 a<b；相同内容指纹的 rejected 关系可被确定性识别；纯算法不读取/保存敏感正文。
- Verification: `gofmt -w` 仅格式化本 slice 编辑的 Go 文件；运行相关 Go 单测；若包级构建仍被既有 file migration 故障阻塞，记录相同编译错误并用可独立纯函数测试或临时最窄验证补充证据，不修改无关文件。
- Review focus: PostgreSQL 唯一性/索引、内容指纹失效语义、算法版本化、设置兼容和用户现有 `AutomaticKind` 改动不得丢失。

### P-002: 落地四轮 Agent 决策和模型决定补帧

- Status: completed

- Outcome: 外部客户端支持严格动作协议；编排器按单动作/四轮/工具预算运行；模型决定合法补帧数量，服务端选择不重复位置；最终标签分析继续复用现有全量分批闭集逻辑。
- Depends on: P-001
- Write scope: `services/ai_tagging_types.go`, `services/ai_tagging_client.go`, `services/ai_tagging_extractor.go`, `services/ai_tagging_service.go`, corresponding tests
- Source anchors: AC-012, AC-013, AC-015, AC-016; D-001, D-002, D-003, D-004; TC-008, TC-009, TC-011
- Acceptance: 每视频决策 <=4；每轮最多一个动作；超额补帧整次拒绝；工具失败成为结构化观察；未知/坏 JSON 使模型调用失败；缺少 action 兼容 finalize；step 轨迹无自由证据正文；闭集候选与自动标签现有行为不变。
- Verification: 相关 Go 单测覆盖动作解析、代表帧选择、最大间隔补帧、预算矩阵、四轮终止、工具失败和旧响应；运行可用的 package/repo Go 测试并记录基线阻塞。
- Review focus: 不允许模型绕过服务端预算；最终标签批次不得只看代表帧；日志不得重新输出 payload；现有候选保存、标签库变更重试和自动标签逻辑不得回归。

### P-003: 提供不落盘的本地临时转写工具

- Status: completed

- Outcome: `SubtitleService` 通过消费方小接口提供 context-aware 临时转写，WhisperX 优先、Qwen 回退、不可用时不安装；与正式字幕共享容量一的本地转写槽，结果只驻留当前 Agent 内存。
- Depends on: P-001
- Write scope: `services/subtitle_service.go`, `services/subtitle_contracts.go`, `services/subtitle_queue.go` only if shared slot integration requires it, new/focused subtitle tests, `app.go` dependency wiring
- Source anchors: AC-001, AC-002, AC-005, AC-006, AC-014, AC-015; D-003, D-005, D-009; TC-001, TC-004, TC-010, TC-011
- Acceptance: 无正式 SRT 写入、翻译、pending force artifact 或自动安装；原始音频不进入外部请求；转写等待和执行响应 context；日志/DB 不含全文；正式 FIFO/取消/幻觉确认保持原样。
- Verification: focused Go tests with fake engine/runtime seams and temp directories；现有 subtitle queue/translation tests；在可构建条件下运行 `go test -race` 覆盖共享转写槽。
- Review focus: 正式与临时字幕边界、临时文件清理、取消和并发所有权、用户现有 `app.go` 文件迁移/短视频启动改动不得覆盖。

### P-004: 完成同源内容召回、外部确认和关系语义

- Status: completed

- Outcome: Agent 的同源工具按时长有界召回、读取/生成视觉指纹、选 Top 5、调用当前外部多模态 API 高置信确认，并事务保存 detected/rejected 优先关系；正式非自动标签仅作为观察。
- Depends on: P-001, P-002
- Write scope: new `services/ai_same_source*.go`, `services/ai_tagging_client.go`, `services/ai_tagging_service.go`, `models/ai_tagging.go`, corresponding tests
- Source anchors: AC-003, AC-004, AC-007, AC-010, AC-011, AC-013, AC-014, AC-015; D-001, D-003, D-006, D-007, D-010; TC-002, TC-003, TC-005, TC-007, TC-009, TC-010, TC-011
- Acceptance: 低清/重编码/空间裁剪夹具被召回并经 fake high 确认；仅语义相似负例不持久化；候选 <=200、外部候选 <=5；相同指纹 rejected 不被覆盖，变指纹可重判；同源标签不直接写正式关系。
- Verification: 同源纯算法、仓储事务、并发/幂等和外部请求捕获测试；相关 AI service tests；检查外部请求无音频且日志无图像/转写正文。
- Review focus: 本地阈值只做召回、外部 high 是强制确认；normalized pair 竞争；用户否认优先；软删除和用户现有迁移路径兼容。

### P-005: 暴露同源审阅接口、未读 UI 和设置说明

- Status: completed

- Outcome: 新 Wails 方法支持有界关系查询、幂等已读和否认；summary 暴露未读数；首页按钮显示徽标；AI 审阅弹窗显示 relation-only 视频组、双方名称/路径/理由和“不是同源”；设置页提供额外帧上限及外发说明。
- Depends on: P-003, P-004
- Write scope: `app.go`, `services/ai_tagging_types.go`, `frontend/src/components/AITagReviewDialog.vue`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/SettingsPage.vue`, `frontend/src/utils/aiTagReview.js`, `frontend/scripts/*.test.mjs`, Wails generated bindings/models only through repository generator or exact additive maintained pattern, `AI-CONTEXT.md`
- Source anchors: AC-008, AC-009, AC-010, AC-012, AC-014; D-008, D-009, D-011; TC-005, TC-006, TC-007, TC-008, TC-010
- Acceptance: 未读数量正确累计和清除；无候选只有关系也显示；双方删除状态可见；否认幂等；没有后台 `alert`；设置保存/加载默认 20 并展示外发范围；现有 AI 审批 UI 和用户现有迁移/自动标签 UI 改动保持。
- Verification: `npm run test:ai-tag-review`, `npm run test:ai-tag-library`, `npm run test:video-list-ui`, `npm run test:subtitle-workflow`, remaining existing frontend scripts, `npm run build`; Wails binding/Go contract tests where可用。
- Review focus: 加法 JSON/Wails 兼容、列表 limit/排序、重复已读/否认、UI 关系去重、无阻塞弹窗和脏工作树顺序集成。

## Integration And Final Verification

- 逐项确认 AC-001..AC-016、D-001..D-011、TC-001..TC-011 均有新鲜证据；任何未覆盖项不得标记完成。
- 运行编辑 Go 文件的 `gofmt`、相关 focused tests、`go test ./...` 和 `go test -race ./services`。
- 运行全部八个现有前端测试脚本和 `npm run build`；新增审阅/设置断言必须进入现有聚合方式。
- 检查 `git diff --check`、`git status --short` 和精确 diff，确认没有覆盖用户现有修改、没有提交生成产物/构建目录、没有敏感 payload 日志。
- 更新 `AI-CONTEXT.md` 只增加本功能事实，保留用户当前标签合并、自动短视频标签和迁移描述。

## Handoff And Residual Risks

- Status: complete
- Blockers: none.
- Residual risks: 已用中心裁剪、低分辨率 JPEG 重编码和不同内容负例校准本地阈值；真实素材的极端时间裁剪不在一期保证内，外部视觉模型质量仍影响最终确认。
- Verification: `go test ./...`、`go test -race ./services`、全部八个前端测试脚本、`npm run build` 和 `git diff --check` 均通过。

## Execution rules for the consuming agent

- Execute slices in dependency order; verify each slice with its exact commands before starting dependents.
- Two slices may run in parallel only when neither depends on the other and their write scopes are disjoint; integrate results sequentially.
- Follow the installed working agreement for verification, review, stop, and Git discipline throughout.
