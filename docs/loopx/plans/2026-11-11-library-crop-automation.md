---
schema: loopx-plan/v1
source: .loopx/intake/2026-11-11-library-crop-automation/requirements.md
status: complete
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: []
  - id: P-003
    status: done
    depends: [P-001, P-002]
  - id: P-004
    status: done
    depends: [P-003]
  - id: P-005
    status: done
    depends: [P-004, P-006]
  - id: P-006
    status: done
    depends: [P-003]
---
# 片库组合筛选与自动黑边工作流

## Goal And Boundaries

用户在设计交付后调用 `$exec`，视为授权按[需求](../../../.loopx/intake/2026-11-11-library-crop-automation/requirements.md)、[概要](../design/2026-11-11-library-crop-automation/概要设计.md)和[详细合同](../design/2026-11-11-library-crop-automation/需求设计文档.md)实施及验证。设计中的技术选择由本次执行请求接纳，用户已裁定的原完整文件名/扩展名/容器、全库后台发现、人工导出及片段另存均保持。无提交、推送、安装或部署。

用户后续明确“先完成实现，空间就绪后继续重验证”：P001..004为实现交付分片，其重验证命令移至P005，未运行范围不视为通过；P005保留全部源场景且完成前整体不能complete。这是验收顺序调整，不删除任何TC。

控制器只维护计划、核查候选、集成、生成绑定和执行验证，不编写功能代码/测试；叶子工作者提供基于明确基线的可审查未应用补丁。并行工作只用独立副本，不在主工作区并发写入。所有本机 Go 命令使用 agent-go，重任务总数最多2。仅使用合成视频和一次性测试库，不访问用户片库数据。

## P-001 目录文件名组合筛选及文件排序

独立字段按AND组合，新增文件名/字节大小升降序及稳定游标；保存视图、随机、计数、版本聚合、清空/命令跳转和前端传递条件保持一致。旧默认与搜索模式保持。迁移旧视图为空串。

> writes: services/library*.go, services/random*.go, services/jellyfin_library.go（仅保存视图字段传递）, services/jellyfin_library_test.go（相关回归）, services/semantic*.go（仅筛选传递）, models/video.go（仅SavedLibraryView字段）, services/testdata/model_schema_golden.json, database/migrator/*test.go（仅相关fixture）, frontend/src/components/VideoListPage*, frontend/src/components/video-list/LibraryToolbar*, frontend/src/components/video-list/libraryQuery*, frontend/src/utils/libraryFilter*, frontend/src/utils/randomPlayback*, frontend/src/utils/*query*, frontend/wailsjs/**（仅控制器生成）
> anchors: AC-01, AC-02, D-LCA-01, TC-01, TC-02
> architecture: 扩展VideoService唯一applyLibraryFilter和library_browse_sort，工具栏仅传条件；无第二查询路径；双库语义及旧游标验证。
> verify: 当前检查补丁边界、gofmt语法、diff和轻量前端检查；下述重验证由用户授权移至P005：agent-go test ./services ./models ./database/migrator -run 'TestLibrary|TestRandom|TestModelSchemaGolden|TestAllModels' -timeout 600s；前端受影响Vitest；绑定生成/守卫与全量在最终验证。

## P-002 原容器整片裁剪编码

profile覆盖默认扩展名，同参数短样本和完整单源crop编码；保持原容器/完整文件名，预检明确encoder、尺寸、帧率、轨道约束；不改旧无策略项目、片段另存或其他编辑类型。recipe新增策略及冻结计划/结果DTO为后续替换发布提供接口，但本切片不执行破坏性发布。

> writes: services/video_edit*.go（不含新增replacement文件）, services/video_edit*test.go, frontend/src/components/video-workbench/EditExportPanel*, frontend/src/utils/videoEdit*（若存在，仅编码摘要）
> anchors: AC-04, D-LCA-03, D-LCA-05, TC-05（原容器编码与历史计划），TC-06（预检失败保护）
> architecture: VideoEditService内部profile与encodeFullCrop复用坐标/轨道/进度；冻结配置驱动预检/实际编码，绕开旧MKV+concat但不新增执行服务；旧三任务保护。
> verify: 当前检查补丁边界、gofmt语法和独立审查；下述重验证由用户授权移至P005：agent-go test ./services -run 'TestVideoEdit|TestEdit' -timeout 600s；每profile真实ffmpeg正例及尺寸/轨道反例，缺encoder显式报错，承重测试有效性检查。
> review: 独立检查冻结profile、旧默认保护和实际容器/codec贯穿，不把muxer可见当成功。

## P-003 原文件替换、恢复与工作台结果保护

整片新草稿默认replace_full、片段另存。逐项原路径/同ID更新，保留人工关系；SHA与日志保护全部故障窗口，外接盘无排他改名有复制发布/复制恢复。所有工作目录清理、重排队、启动扫描/预览与结果页清理调用方都保护未决替换；新增恢复按钮，旧项目仍另存。

> writes: services/video_edit*.go, services/video_service.go, services/video_scan.go, services/video_rename_move.go、services/scan_restore.go（仅共用来源guard调用）, services/thumbnail_service.go、services/thumbnail_exploration.go、services/media_exploration_assets.go（单ID失效/路径读锁及已持锁内部复用）, services/preview*.go, services/playback*.go、services/native_playback_source.go（仅替换路径保护/缓存失效）, services/*replacement*.go, services/bookmark_service.go（仅共享播放取源保护）, services/library*（仅未决源保护）, preview_asset_handler.go（仅预览解析到打开FD的路径读锁）, app_video_edit.go, app.go、main.go（仅启动恢复顺序/在Wails绑定前加载恢复保护）, frontend/src/App.vue、frontend/src/App.test.js（仅编辑完成复用片库刷新事件）, frontend/src/components/VideoWorkbenchPage*, frontend/src/components/video-workbench/*, frontend/src/utils/videoEdit*, frontend/wailsjs/**（仅控制器生成）
> anchors: AC-04, D-LCA-03, D-LCA-05, TC-05, TC-06
> architecture: 编辑服务唯一拥有文件日志及DB事务，复用全局路径锁/维护围栏；同ID条件更新不改人工资料；派生数据按原owner失效，不增加第二恢复服务。
> verify: 当前检查补丁边界、gofmt语法、轻量前端与独立审查；下述重验证由用户授权移至P005：agent-go test . ./services -run 'TestVideoEdit|TestEdit|Test.*Replacement' -timeout 900s；逐个发布/还原/提交边界故障及重启、外部冲突、空间/权限失败；工作台结果消费Vitest；绑定生成后实际消费守卫。
> review: 破坏性发布精确diff独立审查，检查副本归属、唯一旧副本保护、DB结果未知、消费者误删、缓存和维护。

## P-004 全库持续发现与完整接线

默认关闭；开启检查全库并持续处理新/变更版本；复用现有检测算法、任务登记/空闲门/共享槽。检测状态与单素材draft一次事务去重，失败无自动重试、不覆盖人工配方、不触发导出。完整连接启动/扫描/设置/任务中心/关闭/维护，新增模型迁移及双库fixture；更新当前领域合同。

> writes: services/crop_detection*.go, services/video_edit*.go（单素材检测提取/事务内创建/仅新增保留error的pending状态读取，旧bool入口仍fail-closed）, services/background_task_registry*, services/settings*.go, models/video.go（仅设置字段）, models/crop_detection.go, models/schema*, services/testdata/model_schema_golden.json, database/*crop*.go, database/migrator/*crop*.go, app*.go（仅任务/设置/启动/周期对账接线）, frontend/src/components/SettingsPage*, frontend/src/components/settings/*, frontend/src/components/TaskCenterDrawer*, frontend/src/components/video-list/libraryTasks*, frontend/src/utils/{idleScheduling,taskCommands}*, frontend/src/components/video-workbench/*（仅自动来源展示）, frontend/wailsjs/**（仅控制器生成）, docs/context/{library,workbench,ui}.md
> anchors: AC-03, D-LCA-02, D-LCA-04, TC-03, TC-04；集成覆盖AC-01..04、D-LCA-01..05、TC-01..06。
> architecture: 发现worker拥有状态，VideoEditService拥有项目；唯一键/CAS及事务阻止重复草稿；后台登记与现有空闲门/媒体槽/维护共用，失败隔离到单项。
> verify: 当前检查补丁边界、gofmt语法、轻量前端与独立审查，并在资源下限满足时按AI-CONTEXT机械生成完整接线绑定；下述重验证由用户授权移至P005：agent-go test . ./services ./models ./database ./database/migrator -run 'TestCropDetection|Test.*Crop|Test.*Schema|Test.*Task|Test.*Settings|TestAllModels' -timeout 900s；前端任务/设置/工作台测试；双库迁移往返和重复调度/取消/版本变化验证。
> review: 独立检查新表迁移/去重事务、任务生命周期和全部原需求集成。

## P-006 排他改名EINVAL的同卷探测闭环

控制器对现有Unix helper实查发现它只识别ENOTSUP/EOPNOTSUPP/ENOSYS，Linux底层Renameat2原样返回错误；D-LCA-05明确要求EINVAL经同卷小型探测证实后才可视作不支持。补齐该已接受边界，其他EINVAL/权限/I/O不能误判为可复制。只修改replacement私有helper及专项测试，不影响P004接口或共享持久状态，可独立隔离实现，顺序集成。

> writes: services/video_edit_replacement.go、services/video_edit_replacement_unix.go、services/video_edit_replacement_other.go、services/video_edit_replacement_probe_test.go
> anchors: AC-04, D-LCA-05, TC-06（不支持排他改名外接盘）
> architecture: 编辑服务内部复用既有原生rename hook，同卷私有/排他创建探测文件只验证能力；不覆盖现有文件，不新增服务/状态表，不给BT/普通另存增加fallback。
> verify: 当前gofmt/diff和独立精确增量审查；真实/注入的EINVAL已证实不支持→复制、探测成功/权限/I/O→保留原错误及原片、探测文件清理全部待P005重验证。
> review: 独立检查错误归类、探测文件归属/同卷约束、无覆盖和原恢复语义。

## P-005 空间就绪后的完整运行验收

保留原P001..004全部verify中的重命令、承重变异及TC，不以静态审查或实现分片done推断通过。先检查主机至少满足本地资源下限、Docker测试数据库可启动，再恢复真实编码/Go/双库和完整集成检查；若源码接线阶段资源不足未能生成绑定，也在本片补齐。现阶段由用户授权延后，未执行不填通过。

> writes: frontend/wailsjs/**（控制器生成）, 相关失败所需修复返回原owner并记录精确增量，docs/context/{library,workbench,ui}.md（实测范围记录）
> anchors: AC-01..04, D-LCA-01..05, TC-01..06
> architecture: 集成原查询/编码/替换/发现同一实现，不建替代测试路径；每个命令记录源码输入与退出状态，失败不扩大无关修复。
> verify: 下方Integration And Final Verification全部命令与P001..004移入命令，原容器成功/失败、故障恢复、检测去重及双库真实断言；所需测试有效性检查。
> review: 原owner修复的重要增量独立复核；全diff安全/公共兼容最终审查。

## Integration And Final Verification

- 原需求逐项核对；所有profile至少成功合成输入/输出及相关失败；失败注入/重启恢复验证不碰用户文件。
- 按AI-CONTEXT生成Wails绑定并规范化；npm test、npm run build；实际WebView可运行则执行受影响用例，不能以静态测试替代。
- 最终agent-go test ./... -timeout 1500s；所需vet；双库专项/迁移使用一次性PG，若不可用先查明环境，不以SQLite代替声明。
- 共享计算/状态/错误处理测试需先红或承重变异证据，遵循已安装tdd mutation-check；全diff独立审查闭合Important后复验受影响范围。
- 总体架构核查：不增加第二查询/检测/恢复权威，不扩大另存合同，不自动裁剪。

## Handoff And Residual Risks

- Review：P-002..004因公共编码兼容、破坏性替换及迁移共享状态，执行前要求独立只读计划风险审查；普通P-001可先开展。plan_risk_review 已完整独立检查计划/原需求/设计及风险相关代码，P002..004无Critical/Important阻塞；注意recoverVideoEdit紧接工作目录清扫、native_playback_source已有路径读锁。P001扩大jellyfin_library保存视图字段传递，保留同一LibraryFilter合同。
- Blockers：无本次相关未决问题；此前资源/网络限制已用本任务隔离临时库解决并完成P005，历史失败保留于下方记录。
- Residual risks：物理磁盘/第三方程序边界按设计；完整用户图形会话、真实外接盘不由合成测试代替。
- Resume note：baseline HEAD 0c3f19bac6a76bef622b1cab34f537641195c6a2 / tree b4550d49ef9a6f38d72d39e7787b9f481c07e0f8。基线仅本轮设计目录未跟踪，intake为忽略文件；未有源码改动。检查点与日志：`.loopx/workspace/2026-11-11-library-crop-automation/`。P-001候选位于/tmp/cineinsight-lca-20261111-p001，基于/tmp/cineinsight-lca-20261111-base；基线逐文件哈希baseline-manifest.json。Go相关基线通过（./database/migrator本次正则无匹配用例），前端7文件238测试通过，日志baseline-{go,frontend}.log。

执行规则：原需求与设计共同约束；满足依赖、验收/复验及必要独立审查后才将切片标done，全部集成新鲜通过才标complete。命名/内部helper可自主决定，不能减少验收。按当前AGENTS仅为具体失败、变化或覆盖缺口推进；有效证据不重复运行，不增加固定自查/报告轮。Git提交/推送/合并/丢弃均未授权。

### Integrated evidence

- P001：精确13文件候选已集成；agent-go相关Library/Random/Jellyfin/schema/model测试通过，迁移用例TestMigrateLibraryExperiencePreferencesAndFiltersRoundTrip在后续P002命令单包退出0；前端2文件177通过。独立plan_risk_review精确diff无Critical/Important。承重目录条件变异已被回归捕获，游标变异也被排序回归捕获（2个有效承重变异均失败于预期断言，已恢复隔离源码）；双库为最终重验证；绑定在最终源码接线完成后按资源条件机械生成。
- P002：精确12文件候选已集成，Go检查失败于历史另存crop end-to-end的3个fixture（新默认策略需显式回到save_copy以保留其旧合同断言），worker正在修正。13个新容器子用例没有报失败；不把整个失败命令写为通过。日志p002-go.log。

- P002独立精确diff审查发现Important：crop未接轨道drop解决冲突、SDR10位可能静默降8位、ffprobe家族别名无法识别真实容器错后缀。已返回原叶子修复，未解锁P003，不削弱原需求/测试。

- 验收顺序变更依据：当前用户原话“先完成实现，空间就绪后继续重验证”。P002增量修复已集成、gofmt/diff检查与独立复核进行中；重Go/编码/PG验证全部保留P005，不宣称通过。当前accepted-manifest.json/accepted-tracked.patch/accepted-untracked.tar为完整已集成检查点；P001/P002候选及原补丁均保留/tmp/cineinsight-lca-*。

- P002原三项审查修复中，位深/真实容器识别在增量复核静态关闭；剩余crop轨道选择需按scope_video_id隔离素材，避免A文件drop连带B，worker正在最后修订。原始Go结果仍有历史fixture失败，最终修复版本运行检查由用户授权延后P005。

- P002实现分片按用户调整后的验收顺序done：原候选+repair+scope三份精确增量已集成，gofmt/diff检查通过；独立review_design确认3项Important及后续批次scope风险均在静态层关闭。重运行验证仍全部在P005，不能把此done写成编码兼容/回归运行通过。P003开始，基线为当前accepted-manifest.json/accepted-tracked.patch/accepted-untracked.tar，对应P001+P002最终修复状态。

- P002轻量前端最终检查：videoEdit工具和VideoWorkbenchPage两文件40测试通过（请求中的EditExportPanel.test.js不存在，实际输出明确2文件）；不是Go/真实编码验证。日志p002-light-frontend.log。
- P003隔离候选/tmp/cineinsight-lca-20261111-p003，准确基线/tmp/cineinsight-lca-20261111-p003-base（P001+P002最终scope），控制器根目录以accepted检查点为准。leaf p003_replacement 正在实现，禁止重验证，尚未集成。

- P003必要局部writes扩展main.go：现代码database.Init后即Wails.Run，而startup恢复原为异步；需在绑定可调用前加载恢复/保护，避免前端先读半文件。无其他worker重叠，来源D-LCA-05启动恢复约束。其余播放/监听owner待确认精确文件后再扩展。

- P003进一步扩services/bookmark_service.go的共用bookmarkSource：原生/队列播放经其取源，需要拒绝未决替换源。监听复用原video_scan.go范围，不扩library_watcher。

- P003缩略图owner扩services/thumbnail_service.go：旧缓存mtime判定不足以保证替换后失效，新增单videoID缓存失效及请求路径读锁。来源统一guard由VideoService组合replacement与既有BT检查，额外调用方video_rename_move.go、scan_restore.go只改共用guard接线，不把非BT责任塞入bt_path.go，不改BT资格。

- P003扩preview_asset_handler.go::servePreviewMedia：实际HTTP解析与os.Open之间存在替换窗口，需要持路径读锁到FD打开后再释放。此为已有预览owner接线，不改变传输协议或持有整个播放时长。

- P003缩略图防递归锁扩展thumbnail_exploration.go与media_exploration_assets.go：既有ResolveAsset已持路径读锁，不用TryRLock失败返回source_busy引入无关降级；以明确已持锁内部helper复用，常规请求维持原等待合同。

- P003补充已证实刷新接线缺口：video-edit-state目前只有工作台监听，片库/宿主无订阅，关闭监听时替换完成不会更新缓存列表。扩frontend/src/App.vue及App.test.js仅复用既有全局事件/handleVideosChangedElsewhere刷新，不增加另一状态权威。

- P003最终28文件候选已集成但尚未done：/tmp/cineinsight-lca-p003-reviewed.patch；初版完整独立审查快照/tmp/cineinsight-lca-20261111-p003-review-initial，至最终版增量/tmp/cineinsight-lca-p003-final-review-delta.patch。root轻量前端3文件47通过（p003-light-frontend.log），新Go/故障/真实替换未运行。
- P003初审6Important：事务响应未知、completed收尾更新、双目录Sync、预览FD窗口、symlink哈希、重复恢复隔离；最终增量中除收尾状态外均静态关闭。收尾还剩“interrupted用running-only helper清字段”与“删journal后DB清理失败无重试证据”，原leaf p003_replacement正在交准确增量；同步补App全局编辑完成→片库刷新接线。该修复复核通过后才解锁P004。

- P003收尾repair8files已集成（/tmp/cineinsight-lca-p003-repair.patch），root轻量前端App/videoEdit/EditResults 3文件249通过（p003-repair-light-frontend.log）。继续修复：重复Retry无journal时不得重造pending/非replacement误改；所有item级journal检查须派生WorkDir；App完成通知应读取真实item_status。原leaf正在交最后局部增量，运行Go仍延后。

- P003实现分片done（重验证按用户裁定保留P005）：最终28文件+repair8文件+idempotent6文件精确集成，diff/gofmt检查、轻量前端及独立review_design完整初审→各增量复核通过。所有已报Important静态关闭，不能据此声称文件恢复/外接盘/Go运行通过。根检查点accepted-*含全部当前源码。P004开始，必须读取当前P003接口和原需求。

- P003最终idempotent增量已集成（6files316行），独立review_design确认所有未决Important静态关闭；root轻量App/videoEdit/VideoWorkbenchPage/EditResults 4文件276通过（p003-final-light-frontend.log）。这不是Go/真实恢复证据。
- P004隔离候选/tmp/cineinsight-lca-20261111-p004，准确基线/tmp/cineinsight-lca-20261111-p004-base（P003所有增量均集成）。leaf p004_detection负责整个发现/设置/任务接线及当前领域合同，禁止重验证，尚未集成。

- P006为D-LCA05已要求但实查尚缺的EINVAL探测处理，非新产品范围；与P004明确排除replacement文件的写范围独立。原P003检查点保留，后续以P006精确增量补齐同一合同，P005仍保留全部重验收。

- P006生产helper4文件精确集成，review_design完整静态审查无Important，追加两测试源补丁后无生产差异；重运行仍P005。P004初版候选27文件约1190行可预审，尚未集成，leaf仍最后补测试；不可变初审patch为p004-review-initial.patch。

- P004读源DB错误分类须接保留error的pending投影helper，精准扩replacement.go该读取函数，不改恢复/rename。P006已集成并静态完成；为避免旧base覆盖它，P004切换到最新root基线的/tmp/cineinsight-lca-20261111-p004-rebased-base及-rebased候选，先应用原已交稳定32文件补丁再补DB分类；原副本完整保留。

- P00434文件完整候选+事务后通知+符号修正已精确集成；review_design完整初审与所有增量静态闭合，跨切片AC01..04/idle key/启停接线无新Important。最终源helper定义与调用一致；新的Go/真实运行尚未验证。
- 资源变化：当前shutil.disk_usage实读主机可用25.472GiB，已不再低于本机下限。按用户“空间就绪后继续重验证”的条件授权，先完成机械绑定生成，再查明Docker池是否也可启动一次性库；满足实际条件则恢复P005，不把旧环境失败当永久阻塞。

- 资源条件进一步已实测：own redis image只读df探针显示Docker overlay余5.4GiB，主机余25.472GiB；旧no-space条件已经改变。按用户条件授权恢复P005，生成绑定进行中，重新启动同名/同端口/同一次性配置的Postgres，不换目录盲重试、不清共享缓存。

- Wails生成命令exit0，实际App.js/App.d.ts已含Start/Get/CancelCropDetection与RetryEditReplacementRecovery，models.ts含新筛选/设置/scope字段；已按仓库脚本规范化，不手改生成声明。旧服务code未因新AGENTS消息重跑。
- 当前Docker镜像重启尝试失败于Docker Hub GET /v2/ EOF（环境网络），已区别于此前no-space，未假称PG通过；Go/SQLite与真实FFmpeg聚焦最终实现验证开始，输入为全部集成最终source+生成绑定。

- P005真实运行：final-focused-go.log的根/models/database/migrator通过，services失败集中可选semantic元数据表缺席；按原optional storage合同修复2文件并新增presence/absence/error回滚/PGraw向量范围回归，独立review_design静态无Important。final-replacement-go.log：agent-go ./services TestEditReplacement|TestVideoEditCropEndToEnd退出0，7.890s。
- 前端全组件251文件3439测试：3437过，2失败。TaskCenterDrawer旧34数量已修单测试并加强新key；RecapPage月份11/10断言在未改baseline同样1失败14过（baseline-recap.log），本次不修无关既有失败。16项静态守卫全过，生成绑定583导出全部使用。
- Docker Hub宿主curl12秒timeout与daemonEOF确认网络不可用；不再盲拉镜像。改复用已缓存Hindsight镜像内独立pg0运行时，在全新不挂用户卷container cineinsight-lca-pg0-20261111中起PostgreSQL18.1+pgvector0.8.5，loopback15432专用库cineinsight_lca；只改该测试库HBA允许Docker宿主NAT/32，host psql实际连通返回版本。没有触碰既有Hindsight容器/数据/用户片库。

- root TaskCenterDrawer41项复验通过，前端生产构建exit0（仅既有大chunk提示，未扩范围拆包）。生成绑定/16静态守卫有效，私有Go清理修复不改变公开签名，未重复生成。当前frontend/dist已构建，将作为root Go embed输入做一次全量Go（final-all-go.log）。

- 高风险承重5处（legacy/symlink/commit reply/dedup/optional semantic presence）均被目标回归实际捕获，mutation-results.json=5 killed/0 invalid/0 surviving，隔离源码hash已恢复且root未改。Go全量final-all-go.log退出0（services237.093s），vet ./...退出0；新增仅测试的两个new-service startup用例另跑exit0（1.354s），不因追加测试重复全量未变生产逻辑。
- 第一轮PG受共享Docker overlay空间再次耗尽中断；task容器写层仅390MB（db299MB），已停止并移除本次--rm容器，未清其他数据。环境确切改为/tmp有1GiB上限tmpfs、Postgres内存1536MiB/CPU1、WAL128MB，实际df/tmp余979MB；经显式启动配置psql确认fsync=on、synchronous_commit=on。依照外部DB实例/配置已变，PG专项用-count=1确保真实执行非Go缓存，仅补受影响新筛选/四排序/旧游标、去重/恢复和迁移范围。日志final-postgres-tmpfs-go.log。

- PG最终受影响范围（root/services/database/migrator，筛选+四排序/版本聚合/旧评分、去重、替换/新实例恢复、迁移往返）final-postgres-tmpfs-go.log退出0：root1.509s、services17.103s、database0.665s、migrator2.371s。tmpfs使用259MiB余766MiB，未再碰共享池容量；不是此前失败命令的缓存替代。为确认PG向量回归未因可选能力而skip，单独-v执行该唯一用例，属覆盖证据缺口，不重复全域。

- PG向量可选能力覆盖已确认：final-postgres-vector-go.log明确--- PASS（0.44s），没有SKIP，pgvector0.8.5真实向量按目标video_id失效及其他video保留；与普通PG专项合并满足SQL/backend覆盖。新增new-service启动用例SQLite单独通过，禁用启动对账单点变异亦被该回归捕获，累计6个高风险有效变异全部killed且恢复隔离源。
- 进入仅剩的实际并发/race检查（root+services新检测/替换范围），不重跑未变化的常规全量Go；所有已通过输入对应当前生产源码，后续仅新增启动测试文件已单独验证。

- 最终race限定根/services检测/替换范围退出0：root4.465s、services9.007s，无race告警（final-race-go.log）。常规全量Go和双库有效输入未变化，未再机械重跑；仅补既有裁剪WKWebView合成素材套件，源码/用户媒体隔离，之后整理最终结果。

## Final delivery — 2026-11-11

执行配置：继承默认模型与推理设置，无model覆盖；叶子实现，最多2个独立实现并行，独立只读审查。控制器只集成/生成/验证，没有直接编写功能代码或测试。全部AC01..04、D-LCA01..05、TC01..06实现与受影响检查已覆盖，无未解决的相关Critical/Important。

- `agent-go test ./... -timeout 1500s`：exit0；SQLite全量，services237.093s。其后只追加2个startup测试，无生产代码变化；该文件单独exit0，未机械重复全量。
- PostgreSQL18.1 + pgvector0.8.5：隔离测试库、1GiB tmpfs、fsync/synchronous_commit实际on；新筛选、文件排序/旧游标、保存视图/Jellyfin、检测CAS、替换/恢复与迁移专项exit0。向量删除单用例-v明确PASS未skip。环境失败的旧尝试已区分，不算通过。
- 相关race：root/services exit0（4.465s/9.007s）；vet ./... exit0。
- 6个高风险有效变异全部被预期回归捕获（legacy、symlink、commit reply、dedup、optional metadata presence、startup dispatch），0存活/0无效；每次隔离源码hash恢复，root未受变异污染。
- 前端全组件3437通过/2失败；其中本次新增任务数旧断言修正后TaskCenterDrawer41项复验通过，另一RecapPage月份断言在原始baseline同样失败（14通过/1失败），保留为既有无关问题。其他前端输入未变，复用已过组件证据，不重跑251文件只为得到聚合绿字。
- 前端生产构建exit0，16静态守卫全过，绑定583导出/583真实使用。Vite大chunk仅提示，未作无关拆包。
- 真实WKWebView裁剪合成素材10项（加载、像素框、竖屏结果、溢出、像素编辑、比例、无确认门、实时预览、边缘放大、重置）exit0。
- 文档链接/锚点/围栏及git diff --check通过。磁盘/网络问题均未清共享缓存；临时测试数据库仅本任务使用，测试结束停止移除。

完成指本次实现、受影响运行验证与独立差异审查，不表示全量前端无既有失败，也不表示真实外接盘断电或完整应用安装验收。未提交、推送、安装或部署；原用户文件未被测试使用。真实外接盘/第三方占用场景以注入测试覆盖，未宣称物理设备实测。

最终源码检查点仍为 `.loopx/workspace/2026-11-11-library-crop-automation/accepted-*`，baseline HEAD未变；各命令日志同目录。无需重开设计/计划审查；后续仅因实际变化、失败或新的明确需求继续。
