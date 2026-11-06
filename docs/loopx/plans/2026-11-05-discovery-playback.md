---
schema: loopx-plan/v1
source: ../design/2026-11-05-discovery-playback/概要设计.md
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
    depends: []
  - id: P-005
    status: done
    depends: [P-004, P-003]
  - id: P-006
    status: done
    depends: [P-001, P-003, P-005]
  - id: P-007
    status: done
    depends: [P-002, P-003, P-005, P-006]
---
# 动态发现与连续观看执行计划

## Goal And Boundaries

用户2026-11-05明确要求拆分执行计划并实现。原始目标与受保护行为由[概要](../design/2026-11-05-discovery-playback/概要设计.md)和[详细设计](../design/2026-11-05-discovery-playback/需求设计文档.md)拥有，本计划不重定规则。基线master `5b2e8ee`；已有设计目录未提交，保留。此前只push不部署的授权不延伸到本轮：本轮不提交、不推送、不安装。

执行前后均读原设计行为，不以锚点覆盖代替验收。主机重任务串行，Go统一agent-go，前端/原生工具nice10及可用线程限制2；不修改手工/CI设置。不使用真实用户片库作破坏性fixture。

## P-001 原生播放嵌入可行性与固定运行时

在独立fixture中证明libmpv render API与Wails窗口、Web控件/弹窗、字幕、两种全屏及退出释放能共存。产出源码、可重跑脚本和实际结果，不能以单独外部mpv窗口替代。记录固定runtime来源、依赖闭包、最低系统和签名方式，真实RV30/RV40样本身份与首帧/seek基线。任何失败只限制原生接入，按设计修订，不静默降为完整代理。

> writes: `native/playback/`, `scripts/*native*playback*`, `frontend/scripts/native-playback/`, `docs/loopx/design/2026-11-05-discovery-playback/`, ignored `build/native-playback/`
> anchors: D-003,D-007,D-008,D-013; AC-05,AC-06; T-07,T-08,T-09,T-10,T-16
> architecture: fixture独立于生产路由；native owner只持有解码/render资源；Wails原主窗口与控件合同不替换；依赖固定和清理实际验证。
> verify: 可重复native/Wails合成fixture；源/依赖校验、签名、ABI与首帧/seek测量；脚本正常/失败/退出边界。
> review: native source权限、UI叠层及依赖分发，独立只读审查。

## P-002 动态首页与发现页

落实9/12规模、三位置轮换、成功播放补位、软曝光轮次和硬条件、实际曝光与过期请求取消，保留锚点和原随机弹窗。用0/1/2/3/9/12/30/100候选多轮验证不冻结、不伪造新项，首页和视频页均能进发现，详情返回不丢批次。

> writes: `frontend/src/components/viewer/{HomePage,VideoBrowsePage,DiscoverPage}*`, `frontend/src/utils/discovery*`, `frontend/src/App*`, `frontend/src/utils/*navigation*`, `frontend/src/utils/*commands*`, `docs/context/ui.md`
> anchors: D-001,D-005; AC-01,AC-02; T-01,T-02,T-03
> architecture: App拥有有界DiscoverySession，页面拥有批次，复用SuggestTonightVideos与既有版本作品排除；不写观看或新后端推荐状态。
> verify: 定向Vitest与相关静态守卫、构建；新逻辑红灯/变异有效性；真实WebView交互。

## P-003 播放结束推荐与安全导航

增加共享结束画面和整片EOF事件，三项请求与当前作品排除、单次导航、重播、新cycle；队列由服务端给authoritative耗尽事实，区间和探索不误触发。核验来源后打开详情自动播放，确保旧会话退役及统计原owner仅一次。

> writes: `frontend/src/components/player/*`, `frontend/src/components/{PreviewDrawer,QueueInlinePlayer,PlaybackQueuePanel,VideoListPage}*`, `frontend/src/components/viewer/VideoDetailPage*`, `frontend/src/utils/playbackEnding*`, `frontend/src/App*`, `services/playback_queue*`, `services/video*`, `app_video.go`, `app_discovery.go`, `preview_asset_handler.go`, generated bindings
> anchors: D-002,D-006; AC-03,AC-04; T-04,T-05,T-06
> architecture: 播放器只报告事实，宿主请求现有选片，App执行导航；队列owner报告耗尽，来源校验复用受控文件范围；不创建新播放事实源。
> verify: EOF/seek/重复事件/导航竞态组件测试，队列与可见来源Go测试，Wails绑定生成守卫，区间/队列WebKit回归。
> review: 共享queue及来源校验合同的独立只读审查。

## P-004 本机结构分析、存储与恢复

实现无模型自动分段/代表帧位置、两张派生表、generation/run_token/revision检查点、发布与人工章节隔离。覆盖真实FFmpeg/VFR夹具、长短/黑屏/单镜头，以及SQLite/PG并发和恢复。公开最多500段，不把基础分段称剧情摘要。

> writes: `services/video_structure*`, `models/video_structure*`, `models/schema*`, `services/chapter_service*`, `database/`对应schema/迁移/备份测试
> anchors: D-004,D-010,D-011; AC-07,AC-09; T-11,T-13,T-14,T-15,T-16
> architecture: 新VideoStructureService为派生结果唯一写者，复用MediaProbe/受控抽帧；ChapterService仅投影automatic，不写manual；CAS无应用数据库锁。
> verify: agent-go定向服务/model/database测试，真实FFmpeg夹具与双库往返/备份，变异检查发布与checkpoint边界。
> review: 新schema/外键/双库和CAS状态的独立只读审查。

## P-005 全库空闲补齐与产品接入

实现全库存量/新增发现、公平窗口调度、1秒新鲜探测与5秒取消预算、播放资源优先、维护排空及设置/任务中心/章节页投影。字段默认与双库迁移尊重用户关开关，自动不调用云AI/下载模型/ASR。

> writes: `app.go`, `app_*go`相关组合根/设置/维护/章节入口, `services/video_structure*`, `services/idle*`, `services/background_task_registry*`, `services/short_feed_server*`, `services/technical*`, `models/video.go`, `frontend/src/components/settings/*`, `frontend/src/components/*Chapter*`, `frontend/src/utils/{idleScheduling,taskCommands}*`, generated bindings
> anchors: D-004,D-011,D-012; AC-07,AC-08,AC-09; T-11,T-12,T-13,T-14,T-15,T-16
> architecture: 原IdleGate/MediaWorkSlot/Registry扩展既有入口；结构owner自行关门排空，强制空闲新鲜度不改其他服务30秒缓存合同；App只组合生命周期。
> verify: 全库发现/暂停/重启/源变/播放忙等定向Go测试及双库，设置/任务/章节组件测试，实际取消耗时与资源回收。
> review: 维护和后台持续写入边界独立只读审查。

## P-006 原生生产路由与统一媒体适配

在P0结果符合设计的前提下实现owner/window_epoch/session API、源验证、字幕renderer、NativeMediaElement、队列媒体模式、单声源及打包。保留Web路径和明确手动替代动作，不静默转码。退出/维护/重载准确撤销旧请求，原生库不写观看事实。

> writes: `native/playback/`, `services/native_playback*`, `services/preview*`, `services/playback_queue*`, `app.go`, `app_*go`播放/生命周期入口, `frontend/src/components/player/*`, `frontend/src/components/QueueInlinePlayer*`, `frontend/src/utils/{activePlayer,nativeMedia,playbackSubtitles}*`, `scripts/*playback*`, `scripts/build_and_install_app.sh`, `scripts/macos_signing.py`, generated bindings
> anchors: D-003,D-007,D-008,D-009,D-013; AC-04,AC-05,AC-06,AC-09; T-04,T-06,T-07,T-08,T-09,T-10,T-16
> architecture: 原PreviewService选路，新NativePlaybackService只持资源；前端原观看owner/队列报告保持；所有native媒体来源后端验证，无LAN路径；依赖/App合成受控。
> verify: native owner/cancel/seq/来源契约Go测试、控件字幕/队列组件测试、真实Wails播放矩阵/性能、build-only签名及依赖检查，不安装。
> review: 原生句柄、来源权限、生命周期、生成绑定及分发精确diff独立审查。

## P-007 集成验收与文档

核对全部AC与T用例，执行全Go/前端及必要双库/备份/原生/WebView/构建验证。复用有效基线但任何最终改动重新验证受影响部分；已知Recap月份夹具失败单列，不通过弱化检查抹平。记录未实测平台/实际性能范围，不把mock当原生运行。

> writes: `docs/context/`, `AI-CONTEXT.md`, 本计划/原设计的实际实现记录, 定向回归测试和fixture
> anchors: D-014; AC-01..AC-09; T-01..T-16
> architecture: 各owner与合同联合核对，结果回写权威上下文，不复制第二套规则。
> verify: agent-go test/vet所需范围、npm test全部环节、build-only、双库备份迁移、相关WKWebView和原生fixture；最终diff/源码状态和进程清理。
> review: 集成后的安全/共享合同精确diff及修复独立复核。

## Integration And Final Verification

源D-001..D-014、AC-01..09、T-01..16均由上列slice覆盖，不存在已获授权的验收延期。App/绑定/队列为共享写面，按依赖整合，生成绑定始终串行；P0不改变生产播放路径。数据写入采用原门控和CAS；完整运行资源串行。原设计中“仅设计”的阶段描述由本轮实施授权接续，不自动改变未决技术验收。

## 实施过程记录（历史状态，最终结果见文末）

- 设计先前由/root/review_discovery_design独立复核，5项Important已关闭。计划对P-001/P-003..P-007的新来源权限、数据库及共享合同需要实施前风险审查；P-002可独立启动。
- 当前实质风险：本机暂未发现可用libmpv包，需准备固定运行时并验证Wails合成，不能跳过P0。真实RV30/RV40样本可用性须查官方测试素材，不拿改后缀替代。
- 最新有效前端基线来自上一轮：全组件3360通过/1既有Recap月份失败；本轮先建立相关当前基线。尚无本轮Go或native通过证据。
- Resume：首次创建计划，尚未实施；全部修改授权内但不含Git提交/推送/部署。

计划独立审查（2026-11-05）：/root/review_discovery_design核对源设计与AC/D/T覆盖、数据/共享App/绑定依赖后未发现Critical/Important；提示的P-005任务注册和手机传输门控写面已补齐。P-003与P-004广义services/video*有重叠，当前串行执行，未来若分工需先细化文件所有权。
相关前端基线：379项通过（5文件，/tmp/cine-discovery-baseline.log）。

Go相关基线已通过：agent-go test ./services ./models -run Test(SuggestTonight|Chapter|IdleGate|Queue|AllModels)，见/tmp/cine-discovery-go-baseline.log。P-002当前专项374项通过（6文件），新原生fixture尚在构建验证，未接生产路由。

P-002/P-003独立初审发现并处理中：队列退役导致结束组件自卸载取消导航、选择来源指纹未贯穿起播、移除推荐后hero残留。修复须补父子真实挂载和来源竞态用例，再独立复核；不可将已有445项通过当作这些路径的证据。
P0已在实际Wails通过合成AVI/原生GPU帧/暂停/seek/遮挡/应用内全屏/EOF/关闭；一次暖播放首帧91ms。仍待受控发布runtime、字幕/系统全屏与真实RMVB矩阵，不标P-001完成。

P-004核心首轮通过：真实FFmpeg PTS、窗口checkpoint/恢复、取消、ABA拒绝与model拓扑（/tmp/cine-structure-core.log）；新增private phase和representative_ready细化可恢复代表帧步骤，已同步设计。尚未接调度/App/任务中心，未做双库验收，独立核心审查进行中。P-003修复后前端486项通过，源Range/队列Go回归进行中。

P-002/P-003独立复核：三项Important（队列结束层导航、选择来源贯穿、hero晚响应）均已关闭；来源/队列Go专项及486项相关前端通过。
P-004/P-005已接入设置、任务中心、全库发现、空闲新鲜探测和短视频传输门控。SQLite专项、PostgreSQL专项通过。独立复核的三项Important已修正并闭合：来源observer借用原operation的DB并纳入停止排空；设置NULL迁移可重入且不插在旧图片指纹迁移前；定位期间合并后台刷新。面板新增4项回归通过。真实双库备份、全量与有效性检查仍在进行。
P-001固定官方运行时44个arm64/macOS11最低依赖闭包及源码/许可证材料已准备。Wails实窗探针通过像素/DOM定位/裁剪/现有控件/两种全屏/负偏移重叠字幕/EOF/Close。独立审查推动改为串行render队列+非阻塞呈现、有界字幕更新/索引和只初始化一次的函数表；修订探针通过(/tmp/cine-native-render-probe2.log)。真实RV30已取得、RV40下载中，仍待窗口epoch、压力与性能验证，尚未进入P-006正式路由。
全Go首轮仅TestModelSchemaGolden因新增模型快照未更新失败，其他包/测试通过；将按正式生成入口更新快照并针对性复核，不把该轮写成全量通过。

P-001完成（2026-11-05）：固定runtime已装入独立.app，codesign --verify --deep --strict通过；真实Wails签名包的20项合成/输入/字幕/全屏/重载通知/关闭/网络引用拒绝场景通过（/tmp/cine-native-signed-final.log）。AVI、RV30 hi1、RV40 sam各冷/暖20次，首帧P95分别为冷186/168/161ms、暖115/79/191ms，seek P95均≤81ms。冷指新进程/decoder，未清OS文件缓存，样本范围和原始数值见native-probe-performance.json；不外推1080p RMVB或慢盘。RV30 eiqu损坏索引在37.93425s定位失败，保留为失败fixture，不计入正常源性能结论。独立只读复核确认render线程、字幕有界更新、函数表竞态三项Important闭合；生产owner/session/seq/资源撤销仍归P-006，不把P0当正式接入。
真实SQLite/PG备份恢复与SQLite→PG→SQLite往返通过，包含两代分段/私有检查点/设置。结构CAS变异首次存活，补入“旧窗口返回时新run已认领”的真实状态竞态后杀死；面板刷新变异已杀死，曝光重置变异暴露用例缺口，已补下一轮查询断言待复跑。Recover改为200条无大帧字段的分批恢复，状态计数限定当前可见片库；SQLite含205条边界、新旧公平及来源关闭专项通过，PG定向复核待跑。
前端完整组件首轮3389通过，既有Recap月份失败和新任务数量的两个固定断言；数量断言已对齐34/32，定向最终复核待跑。18类静态入口全部通过（发现页面登记与结束处理的准确调用合同已更新）。

P-002至P-005本切片验收完成，仍须P-007联合回归：新增实际WKWebView fixture验证首页9/返回换3/播放补位/发现12/返回保留/换批去重/结束3选项/不自动替用户选/单次来源交接/选择后真视频自动播放，10场景通过(/tmp/cine-discovery-webkit-final2.log)。此检查发现并修复“默认发现页初次挂载清空共享曝光”的问题，有明确红灯及60项定向通过；两个前端变异和Go CAS变异均被有效测试杀死。P-004/P-005分批恢复/可见计数/公平调度在双库专项通过，独立定向复核无新Critical/Important。
P-006开始：新增运行时校验与会话DTO，低层播放器已支持借用已验证文件描述符；签名Wails探针通过fd来源版本(/tmp/cine-native-descriptor-probe.log)。正式owner、前端媒体适配、队列/字幕/源协调和打包尚未完成，不视为老格式正式产品已可用。

最终按原T矩阵核对时补跑P-003队列/区间真实WebView和P-004真实VFR/闪光/渐变/500段上限；两项保持in_progress直至新证据回齐，不把前面的局部验收当成这些覆盖。

P-006接入进展（2026-11-06）：正式GetPlaybackSession入口、owner/window_epoch能力、pending Open排空、源FD、序列命令/viewport/字幕配置、NativeMediaElement、原生/网页字幕renderer、详情/队列媒体模式、播放互斥和实际路径变更port已接。真实签名Wails+生产Vue组件+实际NativePlaybackService/SQLite/受控SRT的12场景通过（/tmp/cine-native-owner-real4.log），包含原生结束3选项→另一原生源自动播放、有效观看单写、改名先释放；新增原生队列连续播放矩阵待跑。217项受影响前端通过。Native/QueueMedia/VideoStructure race通过，三项source协调Important和command并发/runtime路径/Stop重试Important已定向复核闭合。离屏16×16消费被遮挡帧使播放/seek继续，原始呈现帧与解码帧分开。打包副本验证/重签manifest已接，装配3项和构建入口7项测试通过，完整签名构建待跑。

P-007进行中：全Go最终树检查运行中；全前端、双库增量、完整WebKit、实际原生队列/最终签名build-only、变异有效性和最终精确diff审查待收齐。仍不提交/推送/安装。原Recap月份用例既有失败单列。

最终全Go已通过(/tmp/cine-final-go-full.log)。全前端3396通过/1既有Recap月份失败(/tmp/cine-final-frontend-full.log)；所有18类静态入口通过。完整WebKit入口在首个原有virtual_window场景因锚点偏移差1.7px失败，未改动虚拟化实现，待复跑并继续其它独立suite。最终审查又发现并修复native来源失效回调先于统计保护、旧异步命令污染新session两项，55项定向通过；打包目标父目录symlink/材料实际摘要/缺runtime发布仍成功3项已修，新增验证中。正常发布构建现在缺native runtime即失败，不再静默出不完整包。

virtual_window锚点失败已在HEAD `5b2e8ee` 隔离git archive副本复现完全相同数值（-50.625/-52.34375，/tmp/cine-final-webkit-virtual-baseline.log），确认为本轮前既有，保留不改。其余10个独立WebKit suite正在串行验收。

## 最终交付（2026-11-06）

- P-001..P-007完成，本轮未提交/推送/安装/部署。构建产物`build/bin/析微影策.app`（92MB）。完整build-only、持续本机签名及44个native库重签后的Go runtime完整性校验通过（/tmp/cine-final-build-only2.log、/tmp/cine-final-signed-runtime.log）；原build/native-playback输入未被重签器修改。Wails的非编译Go命令由本轮临时compiler adapter转发，build/test/vet仍进入agent-go；GOMAXPROCS=2、GOFLAGS=-p=1、nice10、Swift jobs2。
- 全Go `agent-go test ./... -count=1 -timeout1500s`通过；vet ./...通过。最终Native/QueueMedia/VideoStructure race、PostgreSQL Native/结构/迁移增量通过，SQLite和真实PG备份/双向迁移保留public/staging与检查点。
- 全前端3396项通过/1条既有Recap月份失败；后续仅Native局部审查修正，56项（含宿主统计）及最终10项nativeMedia专项通过。18类静态检查通过，最终关键5类复核通过，579个绑定均有使用。
- 10个独立真实WebKit suite共126场景通过；章节fixture已适配正式GetPlaybackSession入口。原virtual_window锚点偏移失败在HEAD隔离副本完全复现，未改实现/容差。真实签名Wails+正式Vue+实际manager/受控SRT/真实native队列的最终15场景通过（/tmp/cine-final-native-owner-physical.log）。P0合成/字幕/全屏/重载/双player/引用拒绝另通过；120次冷暖样本范围见native-probe-performance.json，冷是新进程而未清OS缓存，不外推1080p RMVB/慢盘。
- 独立审查分别覆盖设计、推荐/结束导航、结构CAS/迁移/调度、native owner/epoch/异步UI/源协调与打包。全部Critical/Important已闭合。最终修正含：来源终态先停止普通统计事件；旧命令/play/retire逐await核验generation；原片实际变更期间保持matching admission屏障；缺组件发布失败；父目录symlink不写包外；材料逐文件SHA而非只信complete标志。
- 有效性检查采用隔离拷贝/Go overlay手工变异：结构CAS、面板刷新合并、曝光新轮次、native owner token、stale state seq、retire generation共6项被测试杀死。CAS及曝光首轮存活暴露的竞态/下一轮请求缺口已补用例后复测，不改生产树制造红灯。相关新逻辑原有真实红灯（viewSession native初始化、默认挂载曝光、来源竞态）保留在临时日志。
- 已知非本轮问题：Recap月份断言；媒体墙1100px锚点差1.7px（HEAD同值）。完整已安装应用TCC升级与其他平台原生解码未验收；本轮不改系统权限，其他平台保留旧路径。

交付清理：本轮独立PostgreSQL测试容器已删除，临时变异/HEAD基线副本已清理；保留验证日志、构建产物与可复用运行时，未修改用户安装版本。

Git交付授权（2026-11-06）：完成上述验收后，用户明确要求“提交并push”；本轮按此提交并推送至origin/master，不安装或部署。上文未提交/推送描述为该授权前状态。
