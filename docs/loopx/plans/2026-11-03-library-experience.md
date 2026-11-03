---
schema: loopx-plan/v1
source: docs/loopx/design/2026-11-03-library-experience/概要设计.md
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
    depends: [P-002]
  - id: P-004
    status: done
    depends: [P-003]
  - id: P-005
    status: done
    depends: [P-004]
  - id: P-006
    status: done
    depends: [P-005]
  - id: P-007
    status: done
    depends: [P-006]
  - id: P-008
    status: done
    depends: [P-007]
  - id: P-009
    status: done
    depends: [P-008]
  - id: P-010
    status: done
    depends: [P-009]
  - id: P-011
    status: done
    depends: [P-001, P-010]
---

# 片库体验完善与内容探索 · 实施记录

## Goal And Boundaries

用户 2026-11-03 原话“开始吧，方案没问题”，已整体接受[概要](../design/2026-11-03-library-experience/概要设计.md)和[详细合同](../design/2026-11-03-library-experience/需求设计文档.md)。交付全部 R-01–09 及 R-10 性能评估，不把分期当缩减范围。提交、推送、安装、部署未获授权。详细方案的隐私、原事实源、统计中立、CAS、双库及备份合同均保持。

当前工作区含资源详情功能的 27 个原修改文件及两份设计文档，原内容快照位于 `/tmp/cineinsight-experience-20261103-baseline/`，无 stash/reset。涉及 BT 和生成绑定的集成保留已有改动，按当前树而非 HEAD 生成。Git base 与文件 SHA-256 位于快照；最终审阅用本次实际增量，避免把用户已有改动归于本次。

顺序安排反映共享 schema、App 生命周期、导航、绑定与播放器状态，非独立路径不能同时写。每个结果连同测试、模型集成、绑定及文档一起完成；不能为后续接口预先暴露未使用的 App 方法。

## P-001 字幕显示与同步台词

详情与队列使用同一字幕显示偏好和同步台词侧栏，长列表虚拟化；偏移、刷新、换源及晚回包正确隔离，不重置播放器或写 SRT。完成窗口/全屏真实 WKWebView 验证。

> writes: `frontend/src/components/player/PlaybackSubtitleControls*`, `frontend/src/components/player/PlaybackTranscript*`, `frontend/src/utils/playbackSubtitles*`, `frontend/src/utils/playbackKeyboard*`, `frontend/src/styles/{tokens,playback}.css`, `frontend/src/components/{player/InlineVideoPlayer,QueueInlinePlayer}*`, `scripts/test_playback_subtitles_webkit*`, `frontend/scripts/playback-queue-webkit/*`, `docs/context/ai-subtitles.md`
> anchors: R-02; D-LE-11; D-LE-04 的既有字幕生命周期
> architecture: SubtitleWorkbench 的严格 cue 投影复用；前端偏好不成为数据库事实；单一 track 与父播放器所有权，真实渲染和会话测试守护
> verify: `nice -n 10 npx vitest run --maxWorkers=2` 指定字幕/播放器测试；设计系统守卫；合成素材 WKWebView 字幕专项

## P-002 按目标准备能力

首次空库与可回访入口提供四个目标，状态由既有健康/任务 owner 投影；页面打开无安装、授权或下载副作用，未知和不支持清楚区分。全库与空库均可跳过引导。

> writes: `app_health.go`, `app_health_test.go`, `frontend/src/components/CapabilitySetup*`, `frontend/src/components/viewer/HomePage*`, `frontend/src/utils/capabilitySetup*`, `frontend/src/{App.vue,utils/appCommands.js,components/settings/*}`, `docs/context/ui.md`
> anchors: R-05; D-LE-14
> architecture: 复用健康快照与命令注册，不增加任务/安装状态源；状态变化用原事件重新读取
> verify: 指定组件与命令测试；`node scripts/design-system.test.mjs`；无副作用调用断言

## P-003 共享筛选与推荐偏好

完整支持未看+收藏+时长条件的查询/计数/保存重开，以及标签正向偏好、预设和原负向优先；唯一键迁移与合并不会产生冲突双向偏好。

> writes: `services/library*.go`, `services/video_random*.go`, `services/recommendation_feedback*.go`, `services/tag*.go`, `models/{video,recommendation_feedback}.go`, `database/*`, `services/testdata/model_schema_golden.json`, `frontend/src/utils/*Filter*`, `frontend/src/components/{TonightPickerDialog*,RecommendationFeedback*,video-list/*}`, `app_library.go`, `app_video.go`, `app_experience.go`, `app_experience_test.go`, `app_media_details.go`, `services/experience_contract.go`, `services/jellyfin_library.go`, `frontend/wailsjs/**`, `docs/context/{library,playback}.md`
> anchors: R-04; D-LE-13; D-LE-15 共享条件; D-LE-05、20
> architecture: VideoService 唯一筛选/抽样 owner；SavedLibraryView 分列持久化，正交条件共享规范化；目标唯一+revision，旧调用默认值保行为
> verify: agent-go test 对 library/random/feedback/tag/schema/migrator 的双库测试；组件/保存视图测试；迁移旧索引与并发正负创建
> review: 共享调用方兼容、唯一键迁移、合并并发精确 diff 独立审阅

## P-004 缺集搜索、下载与关联

作品集缺集进入资源搜索，下载来源事务随 BT 意图记录，未知外部写不重发；未映射文件入库后能明确填写季集，精确预览确认后原子关联，离线不当缺失。

> writes: `services/{collection*,bt*,video_service*,trash*,video_detail*,metadata_batch_edit*,automation_rules_apply*,enhancement_metadata_copy*,media_metadata_merge*}.go`, `models/collection_episode.go`, `models/bt.go`, `models/schema.go`, `database/*`, `services/testdata/model_schema_golden.json`, `app_bt.go`, `app_media_details.go`, `frontend/src/components/{CollectionEpisodePanel*,bt/*}`, `frontend/src/utils/btAPI.js`, `frontend/wailsjs/**`, `docs/context/{metadata,integrations}.md`
> anchors: R-01; D-LE-10、05、20
> architecture: CollectionService 统一成员CAS，BTService 意图/文件owner保持；来源不替代传输事实，新增核心须覆盖所有成员写入方
> verify: agent-go test 双库成员CAS、旧成员写入回归、隔离BT合成资源全链路；UI映射/过期预览；非空往返与备份
> review: 外部副作用顺序、全部成员写入口、原 BT 未提交改动保留与来源范围独立审阅

## P-005 自然语言条件解析

本地与显式外部模式产出可校验草稿，人物重名、角色语义和不支持条件不能被静默丢弃；外发只含查询文本，关闭/切库/换配置后晚回包不可用。保存条件复用共享视图。

> writes: `services/library_query*.go`, `services/text_ai*.go`, `services/ai_tagging_client.go` 的最小传输复用, `models/video.go`, `app_library.go`, `app.go`, `app_settings.go`, `app_functional_polish.go`, `frontend/src/components/{SearchQuery*,settings/*}`, `frontend/src/utils/queryConditions*`, `frontend/wailsjs/**`, `database/*`, `docs/context/{library,ai-subtitles}.md`
> anchors: R-06; D-LE-02、04、15、20
> architecture: 解析器不执行SQL/工具；实体解析在VideoService本地；新增文本适配器只持请求，不接管原打标
> verify: 本地语法和恶意模型输出表驱动测试、假HTTP外发范围/超时/取消、双库组合查询与视图；用户显式启用组件测试
> review: 隐私、配置代次、输入约束与维护围栏独立精确diff审阅

## P-006 专题与受控区间队列

专题组织同一书签，区间入队快照冻结成员/顺序/末项，单条与连续预览统计中立；原整片/IINA路径保护，预览不越界进入后追加内容。

> writes: `models/{viewing_notes,playback_queue,schema}.go`, `services/{bookmark*,clip_topic*,playback_queue*}.go`, `app_viewing*.go`, `app_playback_queue.go`, `app.go`, `frontend/src/components/{ViewingNotes*,ClipTopic*,QueueInlinePlayer*,PlaybackQueue*}`, `frontend/src/utils/{playbackQueue*,appCommands*,viewerNavigation*}`, `frontend/src/App.vue`, `frontend/wailsjs/**`, `database/*`, `services/testdata/model_schema_golden.json`, `scripts/test_playback_queue_webkit*`, `docs/context/playback.md`
> anchors: R-08; D-LE-17、04、05、20
> architecture: BookmarkService关系owner、PlaybackQueue唯一会话owner；CAS与批次身份不依赖前端定时器，原播放事实不可由clip触发
> verify: 双库关系/CAS/旧行迁移、queue原回归与统计零写、重复ended与批次边界；合成视频WKWebView和现有IINA路径
> review: 数据删除语义、原整片兼容、会话与区间统计全增量独立审阅

## P-007 搜索结果汇总与预览

在已有搜索入口上汇总结果，按具体视频分组对白并独立分页，画面top-K明确标示；单条和连续预览共用受控clip，条件/展开/滚动返回保留。

> writes: `services/scene_search*.go`, `app_ai.go`, `frontend/src/components/{CommandPalette*,ScenesPage*,SearchResults*}`, `frontend/src/utils/{unifiedSearch*,sceneSearch*,appCommands*,viewerNavigation*}`, `frontend/src/App.vue`, `frontend/wailsjs/**`, `docs/context/ai-subtitles.md`
> anchors: R-03; D-LE-12、04
> architecture: 原SceneSearch与Library owner保持，新增前端编排；对白数据库分组而非截断后分组，无第二索引
> verify: 双库分组/游标与大命中视频、单组失败UI、top-K范围、单条预览零统计、返回条件
> review: 源版本与旧搜索动作兼容的独立diff审阅

## P-008 章节与可校对摘要

探测原始章节、保存人工章节、生成带字幕证据的草稿；局部应用保留范围外内容，跨界拒绝，任务幂等和取消/维护隔离完备。

> writes: `services/{media_probe*,chapter*,text_ai*}.go`, `models/{media_details,chapter*,schema,video}.go`, `app_chapter.go`, `app.go`, `app_tasks.go`, `app_task_center.go`, `app_functional_polish.go`, `frontend/src/components/{Chapter*,viewer/VideoDetail*,player/*,TaskCenter*}`, `frontend/src/utils/taskCommands*`, `frontend/wailsjs/**`, `database/*`, `services/testdata/model_schema_golden.json`, `docs/context/{ai-subtitles,ui}.md`
> anchors: R-09; D-LE-18、02、04、05、20
> architecture: MediaProbe只持技术来源，ChapterService持人工/草稿；文本适配复用，无自动ASR或外部工具，原字幕事实不复制
> verify: ffprobe合成章节、假模型分段/证据/取消/幂等、双库范围应用与备份恢复、真实播放器定位
> review: 数据丢失边界、外发内容、原任务生命周期与幂等占位独立审阅

## P-009 照片地图与旅行回顾

本地GPS聚合及日期/人物筛选、命名回顾；显式联网栅格底图，不发照片/人物/完整坐标列表，不接地名查询。极区/日期线/空日期不造数据。

> writes: `services/{image_map*,photo_recap*,map_tile*}.go`, `models/{photo_recap*,schema,video}.go`, `app_image.go`, `app.go`, `preview_asset_handler.go`, `frontend/src/components/viewer/{PhotoBrowsePage*,PhotoMap*,PhotoRecap*}`, `frontend/src/components/settings/*`, `frontend/src/utils/photoMap*`, `frontend/package*.json`, `frontend/wailsjs/**`, `database/*`, `services/testdata/model_schema_golden.json`, `docs/context/{ui,integrations}.md`
> anchors: R-07; D-LE-01、16、04、05、20
> architecture: ImageService管可见照片；瓦片适配仅桌面且固定provider URL，无通用代理；回顾存筛选而非媒体副本
> verify: 双库地理/日期线/聚合上限/回顾CAS；假瓦片server请求与缓存政策/网络关闭；实际WebView与网络能力限定验证
> review: 私有坐标边界、SSRF/重定向与LAN隔离精确diff审阅

## P-010 搜索性能评估

在合成规模和两种后端测基线/候选，保持中文短词与特殊字符子串语义；给出有证据的采用或不采用结论，若采用索引先补已批准设计的维护合同。

> writes: `services/search_performance*_test.go`, `scripts/search_performance*`, `docs/loopx/design/2026-11-03-library-experience/性能评估.md`
> anchors: R-10; D-LE-19
> architecture: 评估原查询owner与派生候选，不先增加生产索引服务；规模与实测数据分离
> verify: agent-go限流基准，1万/10万/100万段、每组≥30样本、两后端计划/正确性/p50/p95/内存/存储/写开销；记录实际打包FTS5能力

## P-011 集成、文档与最终验收

全部功能对源需求核对，清除过期状态文案，更新领域合同，完成迁移、备份、完整测试、构建与风险审阅。没有实测的设备/第三方能力明确记录，不由单测推断。

> writes: 上述本次增量的必要修正, `AI-CONTEXT.md`, `docs/context/**`, 本计划与设计实施记录
> anchors: R-01–10; D-LE-00、03、04、05、20 及各功能完整验收表
> architecture: 按真实owner恢复/取消，Wails生成只在消费者齐备后串行执行；所有新实体非空双库往返，原协议不扩展
> verify: `agent-go test ./...`、`agent-go vet ./...`、适用race、PG受影响全套/迁移/真实备份、`nice -n 10 npm run test:components -- --maxWorkers=2` 后串行运行 package.json 中其余 test 子脚本、前端build、限流build-only、macOS合成设备矩阵；源需求逐项证据表
> review: 安全、数据与兼容全增量独立审阅，修复Important/Critical后重验复审

## Integration And Final Verification

保留字幕原文件、原正式观看/随机统计、BT授权边界、手机/Jellyfin协议、双库/备份与系统资源限制。新条件跨计数/查询/随机/保存视图一致；clip跨搜索/专题/字幕不能串会话；文本AI跨查询/摘要不能误发；切库后旧token不能复用。

原始验收来源为详细设计第14节的每一行，包括空、单项、上限、部分失败、重启与并发，不能只按D编号核对。重要测试须先见失败或完成mutation检查。Go重命令统一 `/Users/zhangyukun/.local/bin/agent-go`，前端/原生/生成任务限制同样预算并串行；不清理构建缓存。

## Handoff And Residual Risks

作者已检查功能覆盖与依赖；P-003–P-009、P-011涉及共享资源、迁移和隐私，按 plan2exec 做独立只读计划审阅，记录在此；P-001/002是独立可实施的原有前端投影，不受其它风险项待审阅影响。独立计划审阅 review_experience_spec 已核对完整范围、依赖与风险：无 Critical/Important；已补实际 app_playback_queue.go、生命周期/删除写入方和 npm 限流命令。P-001–003已完成切片验证，其余以以下最新记录为准。

基线：前端组件全量（maxWorkers=2）104.39s：208 文件中207通过，3149测试中3148通过；唯一既有失败 RecapPage.test.js:377 期望2026年11月、实际夹具10月。本次播放器4文件原相关基线通过。P001新增纯函数测试先观察到3项缺实现失败，之后实现。Go和设备基线待受影响包/设备检查，不能默认绿。当前树原 models.ts 有两处既有尾随空白，保留用户原文件，后续生成必须保留原接口增量。

技术风险：真实WebKit字幕样式、模型结构化输出、地图实际网络、双库可用验证环境与FTS5编译能力须据实核对。没有权限或环境证据时，只阻塞相应真实验收，不缩减已接受功能。

恢复说明：先读原需求、源设计和本计划，核对实际diff与最新命令输出；不得重做已验证切片，也不得将实施授权扩展为Git/安装/部署授权。

### P-001 当前证据与隔离调整

6个focused测试文件72测试通过；设计系统守卫通过。隔离副本中移除区间边界、移除手动浏览暂停跟随两个mutation均被断言杀死，原工作区未受mutation修改。真实WKWebView曾暴露台词Esc穿透并已修正；后续两轮均在原native指针验证因 `native pointer requires the foreground test window` 中止，不能记为通过。P-001保持进行中等待该验收；P-002不依赖字幕代码且写范围独立，可单独推进，最终P-011显式依赖两者。

P-001补充：独立实现审阅发现工具嵌入全屏层导致确认框/键盘域问题，已改为等待受控退出全屏后Teleport到body并复审无Important/Critical。`test_playback_subtitles_webkit.sh` 已通过真实解码、字体投影/位置、台词跳转、工具退出全屏、确认框可点击及取消保留编辑9项，截图 `/tmp/cineinsight-subtitles-check.png` 已目视检查。WebKit不通过getComputedStyle(::cue)暴露真实字幕盒，改为投影断言+实际截图；系统全屏原native指针用例仍待前台环境。新脚本非法参数/非macOS边界已验证。

P-002当前：健康状态已有Apple被旧引擎过滤的缺口随引导修复；TestHealth基线1.281s通过、新测试先缺函数失败，修复后TestHealth1.298s通过。前端引导/宿主/播放器共10文件387测试通过（最新返回空库提示刷新改动需追加复测）。设计系统、绑定499/499、data-test守卫通过，无新增App绑定。

P-002完成证据：最后10文件388测试通过（5.92s，含返回首页后空库提示重新判定）；TestHealth1.298s通过，设计系统/绑定/data-test守卫通过。P-003基线：agent-go services Library/Saved/Recommendation/Random/Tag/Version专项6.999s通过；组合筛选新增测试先因字段缺失失败，当前实现与回归执行中。

P-003环境记录：PG数据库升级专项3.252s通过，服务/迁移腿首次因磁盘仅164MiB导致link no space失败，不是测试断言失败。用户明确授权“你清理一下缓存”，持有agent-go共用锁清理3小时前旧Go缓存约4.04GiB（11012文件），最近3小时缓存保留；清理后可用10GiB，恢复验证。清理明细 `/tmp/cineinsight-cache-cleanup-20261103.json`。PG隔离容器 `cineinsight-experience-20261103-pg`，仅合成测试库、127.0.0.1:32820，任务结束清理；不使用任何已有服务数据库。

P-003验证更新：PG数据库3.598s、services6.866s、SQLite→PG→SQLite新字段/偏好往返1.767s均通过（含并发创建正负方向、合并与新增/撤销竞争）；SQLite并发专项5轮1.364s通过。独立review发现命令跳转未清空新条件，已补resetConditionsForCommand及详情/标签/完整度回归，复审无Important/Critical。前端全量211文件3167测试仅既有Recap月份失败及新增字幕select样式守卫失败；后者已修，相关7文件191测试及全部静态守卫通过。全量Go正在运行，最终App错误码按实际维护/重启状态投影的新增测试还需专项复测。

P-003全量Go：agent-go test ./... -count=1 -timeout1500s 退出0，root35.541s、database12.152s、migrator3.764s、services264.282s，其余包通过。补充App维护/待重启错误码与JS安全整数/重复目标检查后，root专项0.813s、services专项1.030s通过；后端核心最后完整增量正在做隔离Go overlay mutation。前端相关191测试及设计系统/绑定502/502/data-test守卫通过。

P-003隔离变异：Go -overlay 在临时副本中分别破坏时长包含上界、负偏好优先、预览签名、待重启错误码，4/4均由目标断言捕获，无存活/无效mutant，原源文件hash一致。明细 `/tmp/cineinsight-experience-mutations.json`。

P-003完成后回归：隔离mutant恢复原文件后root0.831s、services1.811s通过。后续完整集成仍覆盖全部源验收；已批准旧接口不新增外部协议。P-004源码定位：下载选择意图在 services/bt_manifest_preview.go::submitTaskLocked 的 btTaskTransaction 内提交，提交后仅notify；新增来源必须在此事务、通知之前落库。生产作品集创建除CollectionService.CreateCollection外还有local_metadata_apply.go内创建；永久视频删除由trash_service.go::hardDeleteVideoTx执行，需纳入revision更新。

P-001最终补充：原系统全屏队列真实 WKWebView 已通过；字幕专项10项通过，含换播放器源后保留编辑草稿。独立审阅又发现常驻工作台跨视频草稿错配、导航晚回包和生成依赖晚回包，已逐项修复并定向复审无Important/Critical。最新宿主/工作台/生成和补集直接调用方12文件521测试通过；宿主确认与导航两项隔离mutation均被捕获。

P-004当前：基础来源/关联、CAS及旧成员写入口SQLite专项 services5.847s、database0.637s、migrator0.762s、root0.941s通过；真实隔离qB普通/补集两条链路21.454s通过（合成视频，未选文件不入库、未映射待核对、明确关联及重复确认）。PG来源/CAS并发、升级、迁移和真实pg_dump/pg_restore分别 services9.016s、database2.220s、migrator3.477s通过。退休连接提示误判经测试先见失败后修复，独立复审无Important/Critical。迁移用例已进一步改为SQLite→PG→SQLite，新增提交后读失败重试与部分批次回滚测试待最新复测。完整Go正在执行，P-004暂不标完成。

最新前端12文件521测试通过；设计系统、508/508绑定和188基线data-test守卫通过。隔离mutation（工作台确认/导航、补集必须手选、批次文件版本、已补齐集、幂等回执拷贝）6/6被目标测试捕获，原Go源文件hash保持，明细 `/tmp/cineinsight-experience-mutations-p004.json`。第一次导航mutation只触发夹具缺方法而非断言，已修测试夹具后重新执行并捕获实际断言失败。

缓存补充：后续磁盘降至约1.9GiB，沿用户已授权范围再次持有构建锁，仅清理3小时前Go缓存15106项约4.45GiB，保留最近3小时缓存，可用恢复约6.45GiB；`/tmp/cineinsight-cache-cleanup-20261103-second.json`。未清理用户数据、源码或其他项目文件。

P-004完成证据：全量Go退出0，root34.414s、database11.898s、migrator3.739s、services239.549s，其余包通过。最新PG补充（含批次后项变更整批回滚、提交后读失败重复提交只读原意图）services6.771s通过；非空SQLite→PG→SQLite的BT文件及补集来源/根版本往返1.890s通过。12文件521前端测试、静态守卫、真实普通/补集下载及双库实际备份均有上列证据。尚未完成整体P005–011，不能把P004切片完成当作整个项目交付。

P-005进行中：本地/外部结构化解析、实体候选/角色确认、共享筛选检索及保存/应用界面、独立用途开关与TextAI配置代次已接线。默认不联网；响应1MiB、query20秒，无重试/重定向/工具。独立审阅发现整句source_text吞条件与UI缺取消链路，已补原子约束交叉校验以及request_id取消占位；服务1.184s、前端5文件431测试与512/512绑定/设计守卫通过。第二轮发现未知情绪仍可被整句来源吞并，已先见回归失败，正在用实际谓词片段定位保留未解释文字；该修正与中文时长的最新测试仍在等待其他会话构建锁。新增App关闭设置→HTTP取消、双库开关升级/往返/备份测试尚待执行。不得将P005标完成。

P-001最新原生验收：`/tmp/cineinsight-p001-native-final.log` exit0，10项含真实解码、全屏工具退出、换源保留草稿及确认框交互均通过。串行macOS脚本已加入字幕专项且bash -n通过。

缓存第三次：可用降至约1.1GiB时按既有授权与3小时界限清理13115项旧Go缓存约4.83GiB，可用约5.97GiB；锁内执行，报告 `/tmp/cineinsight-cache-cleanup-20261103-third.json`。验证继续遵守共享构建锁；不要终止其他会话进程。

P-005验证证据（最后单点审阅待完成）：整句已知约束与未知情绪吞并均先见回归失败，最终只把可核对谓词/字面实体计入已解释范围，未知要求保留unsupported；取消链路与此修复均经独立定向复审无Important/Critical。最终SQLite服务/App/database/migrator分别1.659/0.833/0.630/0.737s，PG服务/升级/SQLite→PG→SQLite及真实备份分别7.637/1.520/1.818s通过；补充SQLite真实备份与新实体不能注入旧草稿1.257s通过。前端5文件432测试通过，设计/512绑定守卫通过。7个隔离mutation（时长单位、独立用途开关、角色确认、已审实体、提前取消占位、UI明确移除和关闭取消）均被断言捕获，记录 `/tmp/cineinsight-experience-mutations-p005.json`。

P-005最后隐私补充：SQL日志开启时实体名会进入新查询的SQL日志，新增回归先失败；已只对候选查询会话禁用SQL跟踪，不改全库日志。最后Parser/TextAI/Query/Queue/Bookmark合并专项3.824s通过（也作为P006队列与书签基线），该单点隐私修正正补定向独立审阅。实际外部AI仅用本机假服务验证，没有使用用户片库或真实提供方。整体全量验证仍在P011执行，P005保守解析不能保证理解任意自然语言。

P-005收尾：SQL日志单点修正已独立复审无Important/Critical；最终前端5文件432测试通过，7个mutation全被捕获，最后合并Go专项3.824s同时覆盖P006未改的Queue与Bookmark基线。P006开始。书签服务当前是每次调用构造的无状态owner；专题CRUD继续由BookmarkService事务管理，短期入队预览归现有PlaybackQueueService实例与准入生命周期，不新建播放会话owner。

P-006进行中：专题CRUD、成员排序、书签创建并加入专题原子事务、删除影响提示，及受控clip批次队列和UI已接线。审阅指出新增显式DB锁违反合同，已全部改成书签/专题CAS与入队完整快照条件更新，复审无Important/Critical；实际关联增删会提升书签revision，旧删除确认遇新关系冲突。统计污染、移除同批非当前项未停播、局部重排及播放器界面越界均先见回归失败后修复。SQLite相关服务4.666s/models0.474s、UI9文件280测试通过；PG重跑服务31.725s/models0.502s通过，含真实并发交错、提前变化的完整快照CAS和1000项入队上限。合成媒体WKWebView 9项通过，包含原生指针、键盘/台词范围、两种全屏、两段真实解码和单次终点上报；首次失败为测试未等待弹层定位后误点，沿原fixture的60ms定位等待修正。截图 `/tmp/cineinsight-clip-preview-check.png` 正补稳定显示控制条的最终快照。

P-006迁移/SQLite实际备份集成服务5.407s/database0.796s/migrator0.726s/models0.494s/root1.001s通过；新PG备份往返、UI最终独立审阅、受影响全量及必要变异待执行。场景命中→专题的UI入口与P007的新源版本结果共同接线，SaveClipBookmark事务核心在P006先验证，不能提前宣称该场景整体验收完成。

PG环境记录：原384MiB tmpfs一次性容器因多轮DDL/WAL写满进入恢复并退出（非业务断言回归）。核对task标签后仅重建本任务容器，名称/127.0.0.1:32820不变，新ID c62740af...，CPU2、内存1GiB、tmpfs768MiB、shared_buffers32MiB、max_wal_size64MiB/min32MiB/checkpoint30s。仅合成库，任务结束删除；勿触碰其他容器或其他会话重验证。

P006补充：常驻专题页离页时晚到预览及已开弹窗串页的两条回归先失败，修复后3文件16测试通过；独立复审无Important/Critical。PG最新App2.234s/services3.273s/迁移1.884s通过，含实际pg_dump/pg_restore、专题新优先分页和复合主键重复索引报告；本次database选择器无命中，旧行升级需以准确测试名补跑。原生最终9项全部通过，截图已目视检查。入队完整快照CAS、分页方向、复合主键索引报告3个Go overlay变异均被断言捕获，原文件hash不变；记录 `/tmp/cineinsight-experience-mutations-p006.json`。全量Go、原IINA及准确升级专项仍待执行。

P006完成：全量Go退出0（root27.800s/database9.571s/migrator3.141s/services201.686s，其余通过）；原IINA合成整片结束/重播/关闭、旧队列升级及场景原回归联合专项services14.780s/database0.800s通过。P007开始，场景→专题入口随新版结果接线，整体尚未完成。缓存第四次沿授权锁内清理3小时前旧文件16274项约10.41GiB，可用恢复约13.30GiB；记录 `/tmp/cineinsight-cache-cleanup-20261104-fourth.json`。

P007当前：搜索汇总、对白按影片/片内分页、原场景专业入口的共享分组预览、场景保存专题均已接线。统计中立预览复用P006冻结队列，不创建临时书签。独立审阅发现作品集错误私有游标字段、陈旧字幕补位未验证与SQL日志边界；前者已改公开cursor_name，后两者先见回归失败后修复，复审无Important/Critical。分页核对最多1000个候选，最终快照只查已验证集合并保留继续游标。PG App3.356s/services57.113s/database4.775s通过，含2200条对白、不拥挤其他影片、改条件/过期/换库游标、旧原片拒播、实际描述LIKE及字幕重建无正文日志、旧Scene/Library/Subtitle回归和P006准确旧行升级。前端9文件356测试通过；527/527绑定、设计守卫通过。能力未执行不冒充零命中的新回归先失败后修复。隔离mutation与SQLite最终全套待完成，暂不标P007完成。

P007完成：最后全量Go退出0（services202.030s/database9.490s/migrator3.234s，其余通过），前端9文件356测试、527绑定及设计守卫通过。作品集公开游标、游标条件/期限、搜索旧源、排名前来源5个隔离mutation全部被断言捕获，原源文件hash一致；记录 `/tmp/cineinsight-experience-mutations-p007.json`。P008进入实现，源需求章节§11已重新核对，原MediaProbe/Subtitle/任务生命周期基线包含在本轮全量中。

P008当前：原章节投影、人工CAS/保留阅读、字幕证据草稿、范围应用、任务中心及详情/笔记UI已接线。独立审阅的应用SRT提交窗口、可选章节拖垮技术探测、用途开关互相取消和15分钟总截止均已修并复审无Important/Critical；对应回归先见实际失败。进一步自查并复现draft_ready最后窗口的字幕/用途抢先变化，改为同一输入协调+短grant提交，独立复审无Important/Critical，root1.288s/services2.112s通过。

SQLite集成（含非空往返与真实文件备份恢复）root1.267s/services3.189s/models0.483s/database1.081s/migrator2.861s通过；PG扩大相关检查及真实pg_dump/pg_restore root4.243s/services20.278s/models0.498s/database8.543s/migrator33.868s通过（此轮在最后ready窗口修复之前，需补最终相关PG测试）。旧技术快照迁移后仍为“未读取”，人工/草稿/请求占位及私有NS指纹往返保留。实际ffprobe读取合成章节并验证重新探测不覆盖人工稿。

原生最终7项通过（/tmp/cineinsight-p008-native-final.log），含真实原生点击、解码seek、同播放器复用、人工保存不触发外部AI、源变后无旧seek；截图 `/tmp/cineinsight-chapters-check.png` 已目视检查。首次检查错误要求源不匹配且暂停的播放器必须readyState2，实际只预载metadata且正确拒播；按其保护行为改为readyState>=1，前面的正向解码仍要求>=2。新脚本参数/非macOS边界通过。最后UI全组及mutation、Go全量仍待完成，P008暂不标完成。

P008收尾补充：最终前端11文件370测试通过，541/541绑定及设计守卫通过。7个Go隔离变异（发送确认、请求摘要身份、范围外保留、跨界拒绝、cue顺序/重复、尾段、总截止）全部被断言捕获；cue用例最初先被数量上限拒绝，暴露覆盖缺口，改成同数量重复编号后杀死存活变异。另1个前端任务状态更新合并变异也被捕获，源码hash保持；明细 `/tmp/cineinsight-experience-mutations-p008.json`。P008最终PG提交窗口及全量Go待完成。

P008最终扩大验证：全量Go完成，只有Root与services各一处固定任务总数31的旧断言失败，新功能业务用例与其余包无失败（services232.393s，database10.922s/migrator3.867s等通过）。已按新增chapter_summary更新为32并定向重验，不能把该次全量命令记成exit0。最终PG核心root4.091s/services12.363s通过，含最后ready提交窗口；后续新增“片长被技术探测修短后拒绝越界章节定位”的回归先失败后修复，已纳入全量业务用例，正在随任务登记表重验。整体最终全量仍在P011。

P008完成：任务登记表两处计数断言更新后Root/services对应全组通过，新增片长修正后的定位边界也通过。11文件370前端测试、8个隔离变异、原生7项、双库实际备份/迁移与最终PG核心有上列证据；外部AI仅使用本机模拟服务，不将模型服务真实可用性当作已验收。P009开始；重新核对OSM瓦片政策与Leaflet官方稳定文档，选定稳定1.9.4而非2.0 beta，保持原批准的地图隐私与缓存边界。

P009完成证据：本地地图/回顾、专用联网开关、桌面瓦片和UI已接线。独立审阅发现旧筛选点可继续点击、304丢原缓存限制，两项均先见回归失败后修复，复审无Important/Critical。SQLite集成root1.172s/services1.835s/models0.340s/database1.018s/migrator3.469s；PG集成及实际pg_dump/pg_restore root2.725s/services10.748s/models0.502s/database6.887s/migrator29.979s通过。额外人物AND/回顾CAS与零查看写入0.722s通过。

前端7文件374测试、550绑定/设计守卫通过；7个Go overlay变异（日期线、聚合上限、日期终点、失效人物、联网确认、缓存预算、关闭取消）全部被捕获，源hash保持，`/tmp/cineinsight-experience-mutations-p009.json`。原生地图10项通过 `/tmp/cineinsight-p009-native.log`，截图 `/tmp/cineinsight-photo-map-check.png` 已检查；只用合成照片/本机模拟瓦片，无真实OSM抓取。入口隔离、旧库默认不联网、非空往返及缓存不进备份均有测试。真实公共OSM的部署可达性未宣称已验收。脚本参数/平台保护通过。

P010开始：采用独立临时库/PG schema、确定性合成语料，0/100/1万/10万/100万段，每查询组每温度30样本；测现有索引与(video_id DESC,start_time_ms,id)候选。PG现有实现已经创建GIN(LOWER(text) gin_trgm_ops)，本次将其视为现状，不重复宣称新增。实测采用Wails同一Go1.24.9及desktop,production标签检查实际SQLite编译能力。冷态定义为新独占数据库连接（含连接建立），不清系统页缓存或PG共享缓冲，不声称物理冷盘。全量结果ID/位置哈希对照独立字面匹配，另检查改词、删除/恢复与写入成本；不会读取真实片库。

P010完成：完整SQLite64.801s、PG56.265s退出0，各260组统计（每组30样本）及10组完整性记录通过；原始JSON和可复现[性能评估](../design/2026-11-03-library-experience/性能评估.md)已保存。SQLite打包驱动无FTS5；排序候选改善部分稀有词但退化分组，PG原GIN已覆盖稀有词且候选明显增大体积，本轮不采用新生产索引。数据是SQL层及新连接冷态，非端到端或物理冷盘。P011全量验证开始。

P011最终独立组合审阅：按原始27文件快照排除用户已有BT增量，原499个App导出及签名保留；维护/恢复取消、共享筛选、统计中立、LAN入口及P010口径未发现新Critical/Important。普通建议保留：任务中心仅取最近100条章节记录，极端情况下较老的运行任务可能缺少完整进度/取消入口；运行任务及草稿不丢失。全量执行结果尚待本轮命令完成，不用审阅意见替代测试。

P011全量Go最终树验证退出0：root29.130s、services230.986s、database11.640s、migrator4.217s、models0.519s，其余全部通过（`/tmp/cineinsight-p011-go-full.log`）。普通测试明确跳过需显式启用的合成性能评估；P010两次完整运行另有成功证据。前端全量正在执行。

P011补正：最终审阅普通建议也已修复。章节任务中心使用一个SQL读取全部queued/running及最近100条其他记录，历史有界、活动任务不被挤出，前端分页不变。新增App回归先失败，再经root0.948s/services2.136s相关全组通过，定向独立复审无Critical/Important；上段“边界保留”已被本修正取代。前端全量226文件/3244测试：225文件与3243测试通过，仅原有RecapPage月份断言仍失败，未改该无关夹具。

P011继续：任务中心补正PG root2.804s/services7.819s；最终race专项root2.660s/services3.621s，无竞争报告；`agent-go vet ./...`退出0。前端全部静态脚本已执行，唯一旧标签合并正则仍要求原内联target表达式，已更新为冻结target、偏好预览令牌与无偏好原调用的四项断言；正常通过，并用临时负对照确认删除任一合并后端调用都会失败。其他静态脚本通过（550/550绑定、data-test及设计守卫等），无放宽检查。原生全套执行中。


## 最终交付状态（2026-11-04）

P001–P011全部完成，九项功能及性能评估均在原授权范围内交付。源需求对照已补入详细设计第14节；未提交、推送、安装或部署，原27文件工作保留。

- Go全量通过；之后只修改章节任务中心有界历史查询，已用SQLite/PG受影响全组及race重新验证，其他包证据沿用同树未改代码。最终vet退出0。
- 前端全量3244项：3243通过，唯一失败为开始前已确认的RecapPage月份断言。全部静态合同脚本通过；标签合并旧正则已随实际预览流程修正，并验证正反例。构建重新生成后550/550绑定再次通过。
- 最终macOS原生八组均通过：虚拟窗口46、观看笔记8、队列24、字幕10、区间9、章节7、地图10、裁剪8，共122项；使用合成素材与loopback瓦片，地图截图再次目视核对。
- `scripts/build_and_install_app.sh --build-only`在统一资源锁内退出0，包含前端生产构建、Wails生成、Go1.24.9应用编译、Apple AI/超分sidecar打包和稳定本机签名校验。产物 `build/bin/析微影策.app`，未替换或启动已安装应用。
- SQLite/PG实际备份、非空迁移往返、源变与取消窗口的证据见各切片；最终独立组合及任务中心定向复审无Critical/Important。性能评估保留现有生产索引，无新增索引服务或迁移。
- 文档105处本地链接核对通过。非生成文件diff空白检查通过；Wails原生生成的models.ts仍含生成器默认尾随空白（基线也有），未手改生成物，也未把全仓diff检查写成通过。
- 已核验标签和容器ID并删除本次独立PG容器，保留不足1MiB原工作快照及验收日志以便评审。没有清理其他项目或运行环境。

证据日志前缀 `/tmp/cineinsight-p011-`：`go-full.log`、`frontend.log`、`task-{red,green,pg}.log`、`race.log`、`vet.log`、`static.log`（旧正则失败及其余通过）、`tag-guard.log`（补正通过）、`native.log`、`build.log`。P010原始JSON随文档保存，可按报告脚本重跑，不依赖临时容器。

仍未验证的环境范围：真实外部文本模型的质量/可用性、OSM公共网络可达性、完整已安装应用的系统TCC流程；本次没有上传用户照片、片库或字幕。前端既有月份测试失败保留，不能宣称npm全套全绿。这些边界不改变已实现功能和上述合成/本机验收范围。
