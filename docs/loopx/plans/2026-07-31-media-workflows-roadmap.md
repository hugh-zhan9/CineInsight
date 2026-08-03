# CineInsight 本地媒体工作流实施计划

## Source And Goal

- Source: `docs/loopx/design/2026-07-31-media-workflows-roadmap/需求设计文档.md`
- Goal: 按字幕工作台、目录实时监听、本地元数据、AI 质量评估的顺序交付四个可独立验证和关闭的本地工作流，同时保持现有字幕生成、片库扫描、详情维护、AI 审批和数据兼容性。

## Boundaries And Global Constraints

- 字幕首版只编辑同名外置 SRT；保存覆盖当前文件且不保留历史或 `.bak`，但必须检测外部冲突并原子替换。
- Watcher 只承诺可靠本地/直连文件系统；事件是对账提示，不是数据库事实，不实现网络盘轮询 fallback。
- 元数据只读取本地 NFO 和同目录图片；禁止 Token、HTTP 请求、远程图片和元数据外发。
- AI 质量只读取本地持久结果，不调用模型、不训练、不自动调整提示词或阈值，不保存敏感证据。
- 数据库只做加法迁移，不改写历史 SRT/NFO/详情或 AI 结果；功能关闭不删除数据。
- 四项能力按既定优先级串行完成和验收；每个切片完成后运行其完整验证，再开始依赖切片。
- 保持 `libraryPathMutationMu`、`scanSyncMu`、软删除、字幕索引、托管图片和现有 Wails API 的语义。
- 高风险文件替换、路径对账、schema/事务和隐私变更在结束前必须对精确 diff 做独立审查并修复 Critical/Important 发现。

## Execution Slices

### P-001: 严格 SRT 文档与安全保存后端

- Outcome: 后端可严格打开、校验、选区重译和无历史地原子保存同名 SRT；外部修改会阻止覆盖，成功保存刷新字幕索引。
- Depends on: none
- Write scope: `services/subtitle_workbench*.go`, `services/subtitleparser/`, `services/subtitle_*test.go`, `models/`（仅确有需要时）, `app.go`, `frontend/wailsjs/`
- Source anchors: AC-001, AC-002, AC-003, AC-004, AC-005, AC-006, AC-008; D-001, D-002, D-004, D-005; TC-002, TC-003, TC-004
- Acceptance: 严格 parser 不静默丢块；限制、时间和重叠校验生效；翻译结果整批应用；保存比较完整指纹且不产生备份；替换或索引失败返回准确结果；现有宽松检索解析行为不变。
- Verification: `go test -count=1 ./services/subtitleparser ./services`; `go test -race ./services/...`; `go vet ./...`; `git diff --check`
- Review focus: 跨平台原子替换是否可能截断/丢失源文件，TOCTOU 冲突窗口，临时文件和权限清理，保存成功但索引失败的事实表达，翻译是否可能部分写入。

### P-002: 字幕编辑工作台前端

- Outcome: 用户可在独立工作台同步预览、编辑/重排字幕、偏移、查找替换、撤销重做、选区重译并显式保存，且不会无提示丢弃未保存内容。
- Depends on: P-001
- Write scope: `frontend/src/components/SubtitleWorkbench.vue`, `frontend/src/components/VideoPreviewDrawer.vue`, `frontend/src/components/VideoListPage.vue`, `frontend/src/composables/`, `frontend/src/**/*.test.*`, `frontend/package.json`（仅测试依赖确有需要时）, `frontend/wailsjs/`
- Source anchors: AC-001, AC-002, AC-006, AC-007; D-003, D-004; TC-001, TC-004
- Acceptance: 所有已定义编辑命令和会话撤销重做可用；时间定位不改变布局；大列表虚拟滚动；dirty/关闭保护准确；保存/冲突/索引待重建状态可理解；ASS/SSA/VTT/内嵌字幕没有误导性编辑入口。
- Verification: `npm test`; `npm run build`; `go test -count=1 ./...`; 使用挂载组件测试覆盖编辑命令、翻译原子应用、关闭保护和保存状态；`git diff --check`
- Review focus: 编辑操作的边界和撤销栈一致性、视频时间同步循环、响应式状态是否导致脏标记误判、窄屏和长文本溢出、复杂交互是否只有脆弱的正则测试。

### P-101: Watcher 事件源与窄范围对账

- Outcome: 可靠本地根目录的事件被递归监听、合并并在稳定后触发受影响路径对账；新增、修改、跨目录改名和消失复用现有迁移与软删除语义。
- Depends on: P-002
- Write scope: `go.mod`, `go.sum`, `services/library_watcher*.go`, `services/video_service.go`, `services/*watcher*_test.go`, `services/video_service_test.go`
- Source anchors: AC-103, AC-104, AC-105, AC-107, AC-108; D-102, D-103, D-104, D-105, D-106; TC-102, TC-103, TC-104
- Acceptance: fake watcher 可确定性驱动所有状态；单事件不直接改库；750 ms 合并与有界稳定探测可取消；对账不走全根枚举；move 保留关系；关闭无新工作和 goroutine 泄漏；网络/错误根不触发自动轮询。
- Verification: `go test -count=1 ./services`; `go test -race ./services/...`; 针对临时真实目录执行 create/write/rename/remove 集成测试；`go vet ./...`; `git diff --check`
- Review focus: goroutine/channel 所有权、关闭竞态、事件风暴内存上限、递归注册竞态、锁顺序、跨批次移动恢复、软删除误判和 `fsnotify` 平台差异。

### P-102: Watcher 设置、生命周期与状态 UI

- Outcome: 新安装默认开启、已有安装升级默认关闭；扫描根动态重配并展示 watching/unavailable/error/disabled 状态和显式重试。
- Depends on: P-101
- Write scope: `models/video.go`, `database/`, `app.go`, `services/directory_service.go`, `services/settings*`, `frontend/src/components/SettingsPage.vue`, `frontend/src/**/*.test.*`, `frontend/wailsjs/`
- Source anchors: AC-101, AC-102, AC-106, AC-108; D-101, D-102, D-103, D-106, D-403; TC-101, TC-104
- Acceptance: 全新/升级数据库 fixture 的默认值不同且可重跑迁移；设置开关控制生命周期；目录 CRUD 无重启重配；错误状态不冒充实时覆盖；重试幂等；启动/手工扫描始终可用。
- Verification: `go test -count=1 ./database ./services ./...`; `go test -race ./services/...`; `npm test`; `npm run build`; `go vet ./...`; `git diff --check`
- Review focus: 安装类型判断是否稳定、AutoMigrate 默认值是否意外开启旧库、App 启停顺序、目录更新失败时旧注册保留语义、状态事件是否泄露不需要的路径。

### P-201: 本地 NFO/图片解析、领域字段与观察状态

- Outcome: 服务可安全发现和解析本地 NFO/图片，生成稳定 manifest 和字段 diff；新视频只填空，已有视频只标记更新；视频详情可表达简介和受控 artwork。
- Depends on: P-102
- Write scope: `models/video.go`, `models/media_details.go`, `models/schema.go`, `database/`, `services/local_metadata*.go`, `services/managed_image_service.go`, `services/video_service.go`, `services/video_detail_service.go`, `services/*metadata*_test.go`, `app.go`
- Source anchors: AC-201, AC-202, AC-203, AC-204, AC-205, AC-209; D-201, D-202, D-203, D-204; TC-201, TC-202, TC-204
- Acceptance: 解析器有 XML/文件/数量边界并拒绝越界链接；无网络能力；字段映射和忽略列表准确；manifest 可重现；新视频不覆盖非空字段；旧视频不自动应用；图片只以托管相对路径保存并由受控 resolver 提供。
- Verification: `go test -count=1 ./database ./services ./...`; 网络阻断 transport 测试确认无 HTTP；NFO/artwork fixtures 覆盖有效、恶意、超限和优先级；`go vet ./...`; `git diff --check`
- Review focus: XML 实体/指令和路径穿越、manifest 冲突遗漏、自动填空与人工值覆盖、托管图片补偿、schema 空值与索引、普通 JSON 是否泄露本地路径。

### P-202: 元数据映射、批量应用与 UI

- Outcome: 用户可预览字段 diff，确认覆盖和人物/作品集映射，对选中视频批量导入或显式补全缺失资料，并获得逐视频结果。
- Depends on: P-201
- Write scope: `services/local_metadata*.go`, `services/person_service.go`, `services/collection_service.go`, `services/video_detail_service.go`, `services/*metadata*_test.go`, `app.go`, `frontend/src/components/`, `frontend/src/composables/`, `frontend/src/**/*.test.*`, `frontend/wailsjs/`
- Source anchors: AC-206, AC-207, AC-208, AC-209, AC-210; D-204, D-205, D-206, D-207; TC-202, TC-203, TC-204
- Acceptance: 空字段默认选择、非空覆盖显式确认；人物 0/1/N 映射和作品集唯一映射准确；同一来源名批次只决策一次；确认前无实体/关系写入；单视频事务和图片补偿正确；部分成功可重跑；backfill 可取消且一次只运行一个。
- Verification: `go test -count=1 ./services ./...`; `go test -race ./services/...`; `npm test`; `npm run build`; mounted UI tests 覆盖映射歧义、覆盖确认、部分失败和任务取消；`go vet ./...`; `git diff --check`
- Review focus: 映射陈旧与并发唯一冲突、锁顺序、事务外副作用补偿、批量部分成功的结果准确性、人物误合并、UI 是否可能绕过 overwrite 确认。

### P-301: AI 运行归因与同源评估历史

- Outcome: 新 AI 任务和同源展示持久化最小、可审计的模型/版本/耗时/计数归因；旧数据保持 unknown，否认同步更新当前评估且不产生重复样本。
- Depends on: P-202
- Write scope: `models/ai_tagging.go`, `models/schema.go`, `database/`, `services/ai_tagging_service.go`, `services/ai_tagging_client.go`, `services/ai_same_source_service.go`, `services/ai_*test.go`
- Source anchors: AC-301, AC-304, AC-305, AC-306, AC-307; D-301, D-302, D-401; TC-301, TC-303, TC-401
- Acceptance: run 生命周期覆盖成功/失败/中断；候选关联当前 run；同内容重复检测不重复计样本；否认事务更新 relation/evaluation；软删除视频历史保留；持久化和日志中不存在 key、URL、路径、帧、字幕或 payload。
- Verification: `go test -count=1 ./database ./services ./...`; 隐私字段反射/fixture 测试；并发同源检测与否认测试；`go test -race ./services/...`; `go vet ./...`; `git diff --check`
- Review focus: 新历史表是否形成漂移事实源、同源去重和否认竞态、失败 run 收尾、硬/软删除外键行为、敏感数据写入和日志泄漏、旧版本兼容。

### P-302: AI 质量聚合 API 与质量视图

- Outcome: AI 管理提供待审/质量视图，展示带样本数的标签、同源和运行质量，并支持已确认时间与归因过滤，不改变审批状态或触发 AI。
- Depends on: P-301
- Write scope: `services/ai_quality*.go`, `services/ai_quality*_test.go`, `database/`, `app.go`, `frontend/src/components/AITagReviewDialog.vue`, `frontend/src/components/AIQuality*.vue`, `frontend/src/**/*.test.*`, `frontend/wailsjs/`
- Source anchors: AC-302, AC-303, AC-304, AC-305, AC-306, AC-307; D-303, D-304, D-305; TC-302, TC-303
- Acceptance: pending/superseded 永不进入分母；unknown 不被猜测；零分母率为 null；7d/30d/all 和标签/置信度/模型/版本过滤准确；软删除历史可聚合但不可操作；质量加载不会调用 AI client；索引支撑约定数据量。
- Verification: `go test -count=1 ./database ./services ./...`; PostgreSQL/SQLite 聚合 fixtures；PostgreSQL `EXPLAIN` 记录关键查询索引路径；`npm test`; `npm run build`; mounted UI tests 覆盖过滤、空/错/加载状态；`go vet ./...`; `git diff --check`
- Review focus: 分母和时间字段定义、连接导致样本重复、方言差异、软删除过滤误用、p50/p95 算法、只读 UI 是否复用并意外触发审批动作。

### P-401: 四迭代兼容性与端到端收口

- Outcome: 四项能力在同一应用中独立启停并通过全量回归；全新和旧数据库迁移、应用启动关闭、Wails 绑定与文档保持一致。
- Depends on: P-002, P-102, P-202, P-302
- Write scope: `AI-CONTEXT.md`, `README.md`（仅存在用户可见配置/行为需要记录时）, `database/*migration*_test.go`, `app*_test.go`, `frontend/wailsjs/`, `frontend/src/**/*.test.*`
- Source anchors: AC-401, AC-402, AC-403; D-401, D-402, D-403; TC-401
- Acceptance: legacy fixture 经全部迁移后旧字段语义不变；每个功能关闭只停止自身行为；无历史内容重写或 AI 回填；应用退出没有后台任务泄漏；生成绑定与前后端接口一致。
- Verification: `go test -count=1 ./...`; `go test -race ./services/...`; `go vet ./...`; `npm test`; `npm run build`; `git diff --check`; 对完整 diff 执行独立安全/兼容性审查并修复 Critical/Important 发现后重新运行全套命令。
- Review focus: 跨切片生命周期、迁移重跑、默认设置、旧数据和 API 兼容、功能开关的数据保留、文档与实际行为偏差。

## Integration And Final Verification

- 用一个临时本地片库完成端到端场景：外部新增视频/NFO/SRT -> watcher 导入 -> 本地资料填空 -> 工作台编辑并保存 SRT -> AI 结果进入质量聚合。
- 外部改名、持续写入、临时断开、SRT 冲突、NFO 变化、人物同名、图片失败、AI legacy unknown 和软删除视频必须保留各自设计的失败语义。
- 用已有数据库 fixture 验证 watcher 默认关闭、现有字幕/详情/AI 数据不改写；用全新 fixture 验证 watcher 默认开启。
- 逐项关闭 watcher、本地元数据自动处理和 AI 质量入口，确认其他工作流继续可用且表/文件不被删除。
- 重新生成并检查 Wails bindings，运行 `go test -count=1 ./...`、`go test -race ./services/...`、`go vet ./...`、`npm test`、`npm run build`、`git diff --check`。
- AC-401、AC-402、AC-403、D-401、D-402、D-403 和 TC-401 在 P-401 与最终集成中共同覆盖。

## Handoff And Residual Risks

- Status: ready
- Blockers: none
- Residual risks: 不同桌面平台对原子替换、卷类型识别和文件事件的细节存在差异，必须以平台适配单测和临时目录集成测试固定；真实超大媒体库的 watcher 资源上限与质量聚合性能仍需在实际数据上校准，但当前设计会显式报错而不是伪装成功。
- Resume note: 从 P-001 开始，严格按依赖顺序执行；每个切片取得新鲜验证证据后再进入下一个切片。

## Execution rules for the consuming agent

- Execute slices in dependency order; verify each slice with its exact commands before starting dependents.
- Two slices may run in parallel only when neither depends on the other and their write scopes are disjoint; integrate results sequentially.
- Follow the installed working agreement for verification, review, stop, and Git discipline throughout.
