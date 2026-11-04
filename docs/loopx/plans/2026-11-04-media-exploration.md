---
schema: loopx-plan/v1
source: docs/loopx/design/2026-11-04-media-exploration/概要设计.md
status: complete
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: [P-001]
  - id: P-003
    status: done
    depends: [P-001, P-005]
  - id: P-004
    status: done
    depends: [P-002, P-003]
  - id: P-005
    status: done
    depends: []
  - id: P-006
    status: done
    depends: []
---

# 随机画廊与可缩放媒体墙执行计划

## Goal And Boundaries

用户于2026-11-04要求「给出执行计划然后可以开始了」，按已交付[概要设计](../design/2026-11-04-media-exploration/概要设计.md)与[详细设计](../design/2026-11-04-media-exploration/需求设计文档.md)实施。原始需求U1–U5与D-ME-01–09是验收来源；计划不减少设计规定的功能或验证。

交付观看侧探索入口、共用持久化展示进度的画廊和可缩放媒体墙、点击预览及详情返回。无新增AI调用、不要求预选素材、不修改既有随机播放权重/观看事实/媒体数据所有权。保留工作区原有设计文件；不提交、推送、安装或部署。

本机重构建串行，Go build/test/vet使用`/Users/zhangyukun/.local/bin/agent-go`；生成和其他构建沿用同一资源预算。测试素材合成，不读取个人片库，不在用户数据库运行测试。

## P-001 持久化随机、展示确认和双库合同

建立两张状态表和探索服务，完成全库候选、版本组归并、无放回序列、场景预留/释放、展示确认、范围隔离、重启恢复、视口保存、来源取样和结构化错误。CAS及请求回执保证换墙不会因重复请求推进两次；切库epoch拒绝旧请求，维护准入复用既有owner。

空库、单项、24/1024边界、2000项全覆盖、部分曝光、删除恢复、分组拆合、并发冲突和回包丢失须有可重复测试。新表进入schema、双库迁移与备份夹具；不能以SQLite单腿替代双库验收。

> writes: `models/media_exploration.go`, `models/schema.go`, `services/media_exploration*.go`, `services/*experience*test.go`（仅夹具扩展）, `database/migrator/*test.go`（仅夹具/往返验证）, `database/*exploration*test.go`
> anchors: `D-ME-03`, `D-ME-05`, `D-ME-06`, `D-ME-07`, `D-ME-09`; U2/U3；详细设计验证表中的随机、空边界、版本关系、并发和切库场景。
> architecture: 同进程探索服务拥有两表；调用原视频/图片可见性及数据库维护事务，不调用旧随机权重或播放写入。原媒体仍由既有服务拥有；验证schema拓扑顺序、双库往返和无观看副作用。
> verify: `agent-go test ./services ./models ./database/migrator`中的探索及相关专项；本切片交付可在两库执行的同一探索夹具并通过SQLite/模型/迁移专项。临时PostgreSQL执行与实际备份恢复归P-004集成验收，仍为最终完成的必需条件；验证失败先于修复或执行定向mutation检查。
> review: 新表迁移顺序、媒体可见性、CAS/幂等/epoch与历史传播是独立只读审查重点。

## P-002 有界缩略图和只预览播放器

在现有缩略图owner下实现96/256像素小图派生、128MiB有界缓存、探索冷生成准入；旧无参数资源路由保持。只选中项随机取帧，来源变更使旧位置失效。

现有InlineVideoPlayer增加显式browse-preview用途：一个实例、8秒区间、静音起步，无正式观看tracker/进度写回/自动代理。已有playback默认用途保持。测试覆盖正常播放、暂停/拖动/区间结束、换源/隐藏、失败/取消、并发与缓存逐出。

> writes: `services/thumbnail*.go`, `services/image_thumbnail*.go`, `services/media_exploration*.go`（样点与来源校验）, `frontend/src/components/player/InlineVideoPlayer.vue`, `frontend/src/components/player/InlineVideoPlayer.test.js`, `frontend/src/utils/*preview*.js`, `preview_asset_handler*.go`
> anchors: `D-ME-04`, `D-ME-05`, `D-ME-08`; U1/U4/U5；详细设计中的资源预算、来源变化及观看副作用。
> architecture: 资源仍由ThumbnailService/ImageThumbnailService提供；新增小图缓存只存派生物，不建任务队列。播放器保留单owner，新增用途隔离观看记录而不复制播放器。共享默认行为要回归全部直接调用方。
> verify: 缩略图尺寸/缓存预算/取消/来源变更Go专项；InlineVideoPlayer及详情/队列/字幕组件测试；真实WKWebView确认区间停播与不写正式观看。
> review: 原资源路由兼容、可见性、共享播放器默认行为及预览不写正式观看需独立审查。

## P-003 探索页面、二维墙、画廊和导航联通

完成32列有界画布、32–320px锚点缩放、拖动/滚动平移、键盘选择、二维视口加载与曝光确认；画廊24项批次和墙共享scene。曝光必须加载成功、前台、无遮挡且满足50%/300ms；未确认失败不得继续换墙。完整接入类型范围、轮次结束、重新洗牌、不可用占位和错误恢复。

接入大图查看器、视频单预览、取样换画面和详情返回；保留scene/坐标/焦点。App增加薄绑定、导航和生命周期管理，生成Wails绑定而不手改生成文件。退出/维护/隐藏/换库取消迟到响应和媒体工作，正常关闭保存进度。

> writes: `frontend/src/components/viewer/ExplorePage*`, `frontend/src/components/viewer/PhotoLightbox*`（仅可选资源地址/动作与混排导航标签）, `frontend/src/components/explore/*`, `frontend/src/utils/mediaExploration*`, `frontend/src/utils/mediaWallGeometry*`（由P-005拥有，P-003只集成调用） , `frontend/src/utils/appCommands*`, `frontend/src/utils/viewerNavigation*`, `frontend/src/App.vue`, `frontend/src/App*.test.js`, `app_media_exploration*.go`, `app.go`, `app_settings.go`, `app_quit.go`, `app_exploration_quit*.go`, `frontend/src/components/ExplorationQuitDialog*`, `app_experience*.go`, `frontend/wailsjs/**`（仅生成）, `frontend/src/components/ui/*`（仅现有图标注册等必要接入）
> anchors: `D-ME-01`, `D-ME-02`, `D-ME-03`, `D-ME-04`, `D-ME-05`, `D-ME-07`, `D-ME-08`, `D-ME-09`; U1–U5；详细设计所有UI/生命周期场景。
> architecture: App协调既有服务/维护/导航；探索宿主拥有请求代次与当前场景，纯几何仅计算坐标；图像调度最多6在途、视频最多1个，不改旧列表。详情返回沿用原返回栈并保留探索页局部视图。
> verify: 几何/曝光/命令队列与组件专项，App入口/维护专项；生成后`npm test`绑定与data-test守卫；真实WKWebView合成素材的缩放、拖动、预览、详情返回、隐藏和冷缓存表现。

## P-004 集成验收、性能证据和文档交付

按原始用户目标和详细设计验证表逐场景核对最终实现；补充真实WKWebView探索验收入口，使用1024格和10万候选合成数据记录首场景/内存/DOM/媒体并发/交互表现。完成双库、备份迁移、共享播放器调用方回归、格式/lint/全量测试及build-only出包；不替换已安装应用。

> writes: `scripts/test_media_exploration_webkit*`, `scripts/test_macos_webkit.sh`, `frontend/scripts/*exploration*`, `services/*exploration*test.go`, `database/migrator/*test.go`, `docs/context/ui.md`, `docs/context/playback.md`, `AI-CONTEXT.md`, `docs/loopx/design/2026-11-04-media-exploration/*`, `docs/loopx/plans/2026-11-04-media-exploration.md`
> anchors: `D-ME-01`–`D-ME-09`; U1–U5；详细设计Verification Strategy全部场景，无授权延期项。
> architecture: 合成测试验证原owner协作与不变量，文档记录实际状态而非用计划替代证据；缓存、探索状态与正式观看分别核对，无第二套媒体事实源。
> verify: `agent-go test ./...`、受影响并发专项`agent-go test -race ...`、`agent-go vet ./...`；`npm test`与`npm run build`；串行`npm run test:macos`；双库/备份迁移专项；受限资源下`bash scripts/build_and_install_app.sh --build-only`；最终`git diff --check`及独立精确diff审查。
> review: 独立审查最终新增状态/资源路由/共享播放器精确diff，修复Critical/Important后验证并复审受影响内容。

## P-005 二维几何与真实展示判定

将P-003中的纯计算边界单独验证：固定slot坐标、视口裁剪、锚点缩放、平移夹紧、键盘选中，以及加载成功且前台无遮挡的50%/300ms展示计时。该模块不接数据库、Wails或媒体生成，接口来自已接受D-ME-02/08，因此可在本机重构建等待期间独立编写。

> writes: `frontend/src/utils/mediaWallGeometry.js`, `frontend/src/utils/mediaWallGeometry.test.js`, `frontend/src/utils/mediaExposure.js`, `frontend/src/utils/mediaExposure.test.js`
> anchors: `D-ME-02`, `D-ME-03`, `D-ME-08`; U3/U4；几何与部分曝光边界。
> architecture: 纯函数及前端短生命周期计时owner；无全局状态、数据库或资源请求，P-003只消费其坐标/展示结果；原一维列表不变。
> verify: 新模块单测，覆盖非边缘锚点误差≤1px、32–320px、末行/空/单项、隐藏/失焦/失败图/迟到计时；定向mutation或失败先行证明断言有效。

## P-006 字幕能力提示修正（用户追加）

按用户明确选择，修正macOS15的错误解释、识别语言标签和操作提示，说明准备按钮只检查当前本机能力，添加听写语言不保证离线识别可用。保持原错误码、执行路径和requiresOnDeviceRecognition=true；不做在线识别。依据及本机版本限制记录在[字幕上下文](../../context/ai-subtitles.md)。

> writes: `services/apple_ai.go`, `services/apple_ai_test.go`, `native/apple-ai/speech_legacy.swift`（仅注释）, `frontend/src/components/settings/SubtitleSection*`, `frontend/src/components/video-list/SubtitleGenerateDialog.vue`, `docs/context/ai-subtitles.md`
> anchors: 用户追加“核实并修正文案，保持本地识别”；既有macOS15本地识别合同继续有效。
> architecture: 只改原错误映射和原UI宿主，语言状态协议、请求和音频处理owner不变；不新增网络或模型依赖。
> verify: `agent-go test ./services -run 'TestApple'`及字幕设置/生成弹窗专项；检查Swift diff仅注释、requiresOnDeviceRecognition=true未改。官方资料支持能力判定，不把用户粘贴的15.7.2数据冒充当前27.2复测。

## Integration And Final Verification

- U1–U5按概要原话验收，重点是零新增AI、无需预先整理、全池随机、固定位置缩放、可进入详情并返回。
- 直接调用方覆盖旧随机播放、默认InlineVideoPlayer、PhotoLightbox、全局队列、旧图片/视频列表、原资源路由。
- 资源/状态初始化、生成绑定、schema与迁移夹具顺序集成；本机重任务不得并行。
- 区分既有失败与本次回归。AI-CONTEXT提到的RecapPage月份断言仅作为调查线索，仍以本次基线为准，不扩大修无关功能。
- 最终检查所有新增绑定都有真实前端调用，无独立服务/模型下载/全库预热/自动转码，无绕过可见性或正式观看owner。

## Handoff And Residual Risks

- Review：作者来源/覆盖检查及独立只读计划、数据/资源、页面/App审查均已完成；6项Important逐项补回归、修复并复审，最新局部修正无新增实质问题。审查记录及证据见下文。
- Blockers：无本次交付阻塞。保留既有RecapPage月份断言失败，未改无关回顾功能。
- Residual risks：已安装完整应用、用户真实片库/外接盘及macOS15语音语言资源未现场验收；合成性能数字不作生产保证。
- Resume note：实施和验证完成，未提交/推送/安装/部署；应用包位于build/bin/析微影策.app。后续工作先核对当前源码与本记录，不重跑前期计划审查。

### 计划独立审查记录（2026-11-04）

只读叶审查者 `/root/exploration_plan_review` 完整审查计划、源设计与相关源码，提出4项Important：样点缺接口、仅最近回执不能识别历史UUID重绑、正常退出缺前端flush、既有资源读取不自动校验探索范围/来源。已在编码前补入D-ME-07（sample及revision绑定身份）、D-ME-08（探索资源上下文与描述符核验）、D-ME-09（有界退出握手），并修正P-002资源处理器写域。审查者复核4项均已消除，允许执行；其余覆盖、迁移顺序及直接调用方验证无新增阻塞。

P-005是P-003纯计算部分的独立验证切片：因其他会话占用本机构建锁，先编写不依赖数据服务的纯逻辑；不扩大或推迟验收。

### 实施证据（持续更新）

- P-001 SQLite专项已通过：`agent-go test ./services ./models ./database/migrator -run 'Test(MediaExploration|AllModels|MigrateMediaExploration)' -count=1 -timeout 300s`。首次执行捕获GORM查询复用导致的含糊列名，修正后通过。原观看事实无副作用、2000项全覆盖/无放回、部分曝光、幂等/并发、空/小池、重启、分组拆分、范围核验与视口均已覆盖。
- P-005 两文件7项纯函数测试通过；mutation检查保留在最终测试有效性验收中。
- 临时独立PostgreSQL容器 `cineinsight-media-exploration-pg-20261104`，只绑定127.0.0.1:33058，512MiB/1CPU，空测试库；不接触已有postgres实例。双库命令已提交，等待其他会话释放Go构建锁。集成验收不得将该等待当成通过；结束清理本任务容器。

- 资源owner/播放器接口已经落地，页面与其接口可按固定合同独立接入；P-003不等待P-002的Go排队验证，P-004必须同时依赖P-002/P-003通过。共享文件由主代理串行整合，最终未免除任何资源验证。P-001 PostgreSQL同一专项与双库往返已通过（services 22.056s / models 0.561s / migrator 3.273s）。

- P-002 Go资源/缩略图/探索专项通过；播放器2个新用例先失败（原实现会自动请求代理并创建正式tracker），修改后连同详情和抽屉3文件186项通过。当前批次24个entry_id纳入视口保存，保证混合墙已曝光项过滤后仍能精确恢复画廊批次（现有view_state边界内的字段补全）。

- P-001/P-002独立精确代码审查 `/root/exploration_plan_review` 提出3项Important：代表离组、隐藏成员离组后恢复丢展示历史、LRU触碰mtime误伤在途读取。3条回归均先复现失败，修复后`agent-go test ./services -run 'Test(MediaExploration|ExplorationThumbnail)'`通过，叶审查复核原3项均消除。历史快照仍在同一entries表按轮清理，缓存LRU不再修改内容mtime。
- P-003已写页面、控制器、导航、退出握手和资源处理器，绑定已通过受限Wails生成。前端初轮147项中145通过：导航清单需要加explore（已更新）；预览用例暴露测试桩structuredClone不能接Vue代理数组，已改为与Wails相同的JSON序列化，待重跑。
- 正在运行的App相关Go专项统一exec session 55616（等待共享构建锁）。原P-001临时PostgreSQL容器仍仅用于本任务，验收结束必须移除。新增native探索fixture与脚本已写，尚未执行。

- App相关Go专项通过（含探索绑定、范围资源、退出握手和原退出/预览回归）；P-003主要页面/控制器/退出相关专项已通过。首次真实WKWebView14场景通过：1024轻量索引，首屏231个DOM格，冷资源首帧批899ms，平移frame P95 18ms（合成资源与桥接，非用户片库）。
- P-003独立审查指出外层滚动离屏曝光、不可用卡片保留旧Blob、无内嵌能力时静态取样分支不可达；4个对应断言先复现失败，修复后两文件9项通过。另修正同scene显式放弃未确认记录后的曝光tracker重置；后续复审及完整WebView复跑属于P-004。
- macOS串行runner原测试夹具只列4套而真实入口已有8套，基线test_success失败；本次加入探索套件时同步夹具为9套，正常/失败/参数/平台4项通过。
- Wails生成新增空行携带制表符，新增`normalize_generated_bindings.py`作为生成后格式化步骤，只清理diff新增空白行，不手改声明或旧输出；4项脚本测试与git diff --check通过。

- P-003复审确认原3项Important及同场景恢复问题均消除；审查者未运行测试，具体测试证据仍以执行输出为准。资源/状态/播放器与页面/App两轮独立精确diff审查均已完成，后续只复核新改动或新发现。
- 全量前端验证通过共享构建锁串行执行package.json中所有原test入口，组件入口只增加`--maxWorkers=1`以限流；单项失败仍继续收集其余检查，结果在`/tmp/cineinsight-exploration-frontend-results.json`，不可把部分通过写成npm test整体通过。

- 为避免多个本机重验证命令同时排队，先撤销了尚未开始的全量前端等待（session 79494，未执行测试），等待SQLite集成/10万候选测试完成后再启动。全量结果文件尚不存在不代表通过。当前唯一等待中的重验证为session 52756。

- 实际备份恢复测试复用现有FunctionalPolish SQLite/PG执行器，夹具加入探索state/entry及不可见history，恢复后核对原样点、视口、回执和成员历史。测试代码已接入，真实执行待本机锁释放。迁移夹具也加入history行，未另造备份实现。

- SQLite集成/真实备份恢复和10万候选专项通过（服务包4.781s）。100000合成图片候选：首scene1322ms，恢复7ms，测量点Go heap_alloc53.78MiB，累计新增分配385.70MiB；这是合成ID查询/建序列测量，不是用户磁盘解码或整进程峰值。
- 全量前端首轮：3282/3285通过，17个入口中仅组件入口失败；两个本次接入问题（App导航断言、MediaTile缺btn-plain）已修正，四文件240项再验证通过。另一个RecapPage月份断言是既有日期依赖，组件与测试均与HEAD相同；不改无关回顾功能。其余16个原test入口全部通过。
- 补齐空库添加素材后可通过“查看新内容”进入首scene（失败断言先复现，再修复），以及失败缩略图的显式重试；不自动重试、不重置其他scope。实际媒体/备份数据未触及用户片库。

### 用户追加：字幕文案核实（2026-11-04）

用户在异步澄清中选择“核实并修正文案，保持本地识别”。此有界追加只改apple_speech_dictation_missing说明、字幕设置引导和识别语言标签；保留错误码、prepare/transcribe分支与requiresOnDeviceRecognition=true，不新增联网识别。官方API只能证明当前离线能力不可用，不能断定模型缺失；本机27.2，粘贴的15.7.2探针数据没有复测。对应Apple错误单测、字幕设置与生成弹窗检查在最终验收完成；无需另建执行计划。

- 最终PG专项通过：services21.244s、models0.522s、migrator2.723s，包含真实PostgreSQL pg_dump/pg_restore备份恢复（2.46s）与最新history/空库导入回归；10万候选性能用例在此轮未设置opt-in，因此跳过，已有SQLite专门测量。
- 全量Go首轮：根包、database、migrator、models和其余子包通过；services唯一失败TestModelSchemaGolden，提示新增两张探索表需要按既有生成入口更新快照。正在用UPDATE_MODEL_SCHEMA_GOLDEN生成，不删除或弱化守卫。字幕追加文案在该轮启动后修改，将另跑Apple/字幕定向检查。

- 最新局部独立复核已通过：空库发现新内容、失败图片显式重试、未知片长处理、同scene引用变化缓存、字幕纯文案和仅新增两表的golden；未发现实质回归。
- 模型快照生成后`agent-go test ./services -run 'Test(Apple|ModelSchemaGolden|MediaExploration|ExplorationThumbnail)'`通过（4.743s），覆盖追加字幕消息。待字幕UI专项、并发/race/vet、全部macOS验收和build-only最终输出。

- 字幕消息/设置与生成弹窗、探索最新UI四文件42项通过；对照单独运行的RecapPage仍复现既有月份断言（期望11、fixture实际10），源码与测试未改。P-006不新增联网识别，macOS15硬件/语言资源状态未实测。
- 临时PostgreSQL容器e752b0ca014d已按精确ID停止并由--rm清理，保留已有Docker服务不动。

- 最终探索并发专项`agent-go test -race ./services . -run 'Test(MediaExploration|ExplorationThumbnail|ExplorationBindings|ExplorationClose)'`通过（services6.581s / app2.415s）；`agent-go vet ./...`通过。
- 正在用Go overlay与隔离JS副本做定向mutation：无放回筛选、根revision CAS、50%曝光门槛、缩放锚点。不会改工作区生产文件或清理编译缓存。

- 定向mutation完成：无放回筛选、revision CAS、50%曝光门槛、缩放锚点4/4均被断言杀死；Go采用overlay，JS采用独立临时副本，工作区生产源hash不变，无存活/无效mutant。新增同scene陈旧revision回归的未变异基线通过。结果`/tmp/cineinsight-exploration-mutation-results.json`。

- 9组真实WKWebView全部通过（串行入口），探索17场景覆盖真实解码、原生点击/拖动、Ctrl+滚轮与gesture事件、外层滚动不曝光、区间停止/seek约束、无正式观看/代理副作用、详情返回与退出flush。最终截图已人工检查并保存到设计目录“媒体墙验收.png”，使用合成素材/模拟桥接，不读取个人片库。
- 探索最终native指标：1024条轻量场景索引，初始DOM231格；合成HTTP/SVG首批可见画面42ms，首次展示保存759ms（含曝光阈值与合并发送），平移更新等待下一帧P95为19ms。该数据不是生产磁盘冷解码或完整已安装应用性能保证。
- 正在受限资源下执行build-only，使用已生成并校验的绑定（-skipbindings）与现有依赖（-m），不会调用安装阶段。

### 最终交付（2026-11-04）

- 受限资源build-only完成darwin/arm64构建、前端编译、Apple辅助程序装配及稳定本机签名验证，产物`build/bin/析微影策.app`；未替换/启动已安装应用。绑定已先生成并通过555/555使用守卫，构建使用-skipbindings/-m保留该输出与既有依赖。
- 最终脚本检查8项通过（生成格式化4、macOS串行runner4），设计/数据钩子/绑定守卫通过，81个文档链接检查通过，新增文本文件与全diff空白检查通过，所有改动Go文件gofmt无差异。
- Go全量首轮唯一失败为新增两表的schema golden，按既有入口生成后，模型/Apple/探索专项复验通过；其他全量包结果保留。完整前端入口均执行：原有RecapPage月份断言仍失败，两个本次接入断言已修复并由四文件240项复验；追加字幕和最后探索四文件42项通过。不能把该前端基线写成全绿。
- 双库状态/迁移、两后端实际备份恢复、race、vet、4项定向mutation、9组真实WebView及构建均有实际成功输出。测试数据库已清理，没有读取个人片库。
- 用户追加字幕范围已完成：只修正文案与语言状态标签，原错误码和本机识别执行路径保留；macOS15.7.2探针数据未复测，不承诺资源下载或韩语识别可用。
