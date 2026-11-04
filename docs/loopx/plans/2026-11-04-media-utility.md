---
schema: loopx-plan/v1
source: .loopx/intake/2026-11-04-media-utility/requirements.md
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
    depends: []
  - id: P-006
    status: done
    depends: []
  - id: P-007
    status: done
    depends: [P-002, P-003]
---

# 看图处理与空间治理实施计划

## Goal And Boundaries

实现[八项原始需求](../../../.loopx/intake/2026-11-04-media-utility/requirements.md)，依据[概要](../design/2026-11-04-media-utility/概要设计.md)与[详细合同](../design/2026-11-04-media-utility/需求设计文档.md)。本计划用于跨功能实施与中断恢复，不是另一次产品批准。文件副本先比较，用户确认后才经原清理处理原文件；不实际操作用户媒体进行验证，不提交、安装或部署。

## P-001 今晚看什么数量配置

在既有弹窗增加1～12的合法输入，默认6并记住本机选择；原过滤、算法和换批行为受保护。非法/损坏偏好与陈旧回包有针对性检查。

> writes: `frontend/src/components/TonightPickerDialog*`, `docs/context/playback.md`
> anchors: AC-06, D-MU-07, TC-09
> architecture: 复用SuggestTonightVideos及原请求代次；仅前端独立偏好，不新增App或服务；原随机条件不增加字段
> verify: `npx vitest run src/components/TonightPickerDialog.test.js --maxWorkers=1`；原相关调用方回归

## P-002 共用图片视口与同步对比

浏览缩放/平移/临时放大、信息栏、胶片条与有界预加载可用；管理选择两张与查看器选图均可进入同步对比，换源/关闭清理正确，查看统计保持原口径。

> writes: `frontend/src/components/viewer/PhotoLightbox*`, `frontend/src/components/viewer/PhotoBrowsePage*`, `frontend/src/components/PhotoLibraryPage*`, `frontend/src/components/Image*`, `frontend/src/utils/image*`, `scripts/test_macos_webkit*`, `docs/context/ui.md`
> anchors: AC-01, AC-04, D-MU-01, D-MU-02, TC-01, TC-02, TC-07
> architecture: 共用视口数学与组件，宿主继续拥有列表和ID；不复制图库状态，不让预加载和对比写查看记录
> verify: 针对性视口/组件测试与真实WKWebView手势和两图夹具；保留原data-test钩子

## P-003 查看中编辑与副本导出

实现完整轻量工具集、原图对照和撤销/重做；预览与导出参数一致，文件保护和取消失败可重复验证，真实大图边界明确。

> writes: `frontend/src/components/Image*`, `frontend/src/components/viewer/PhotoLightbox*`, `frontend/src/utils/image*`, `services/image_process*`, `services/media_processing*`, `services/image_service.go`, `app_image*`, `app.go`, `app_settings.go`, `app_quit.go`, `app_task_center*`, `frontend/wailsjs/**`, `docs/context/library.md`
> anchors: AC-02, D-MU-01, D-MU-02, D-MU-03, TC-03, TC-04
> architecture: ImageService拥有图片来源与输出，MediaProcessingService唯一拥有临时请求/产物/取消和epoch，App负责保存选择与维护/退出握手；复用原图读取与临时发布经验，不引入持久工程库
> verify: 合成素材的参数/方向/透明与导出回归；文件原图别名/重名/失败注入；绑定生成与前端消费者守卫；真实WebView编辑检查
> review: 原文件不覆盖、可见范围、保存路径与取消发布竞态的独立只读diff审查

## P-004 图片严格无损压缩

支持的输入格式经保真核对后才开放，明确报告不支持及无进一步收益。另存可比较副本，原文件仅用户另行确认后经原清理处理；压缩能力随包验证。

> writes: `services/image_compress*`, `services/image_process*`, `app_image*`, `frontend/src/components/Image*`, `frontend/wailsjs/**`, `scripts/build_image_tools.sh`, `scripts/bundle_image_tools.py`, `scripts/test_bundle_image_tools.py`, `scripts/build_and_install_app.sh`, `docs/context/library.md`
> anchors: AC-03, D-MU-03, D-MU-04, TC-04, TC-05, TC-06
> architecture: 与图片导出共用发布保护，格式优化独立纯处理适配器；不向清理owner写候选状态或绕过确认
> verify: 格式像素/元数据/位深/动画边界；收益为零/负收益；工具缺失和实际发布包能力；对比与原文件保留回归
> review: 严格无损证明、原文件保护与打包依赖独立审查

## P-005 视频缩略图时间预览

视频浏览、管理和相关卡片接入共用时间预览；用户展开才生成概览，点击明确时间点沿原预览定位，悬停不写观看。

> writes: `frontend/src/components/Video*`, `frontend/src/components/viewer/VideoBrowsePage*`, `frontend/src/components/RelatedVideoItem*`, `frontend/src/utils/videoThumbnail*`, `services/thumbnail*`, `docs/context/playback.md`
> anchors: AC-05, D-MU-01, D-MU-07, TC-08
> architecture: 复用ThumbnailService取帧、精灵图与原预览入口；交互请求有界，不新增源视频播放会话或另一个缓存owner
> verify: 时间映射、请求合并/失活/失败与陈旧回包；预览定位和无统计副作用；原列表虚拟窗口回归

## P-006 按磁盘与目标生成空间方案

原体检内增加目标输入、真实磁盘空间和可审阅候选，去重并保留锁，候选不足和估计/实测差异可观察，处理连接原清理owner。

> writes: `services/storage_report*`, `services/storage_plan*`, `app_library*`, `frontend/src/App.vue`, `frontend/src/components/VideoListPage*`, `frontend/src/components/video-list/CleanupReviewPanel*`, `frontend/src/components/PhotoCleanupPage*`, `frontend/src/components/insights/Storage*`, `frontend/src/utils/cleanupSelection*`, `frontend/src/utils/storage*`, `frontend/wailsjs/**`, `docs/context/library.md`
> anchors: AC-07, D-MU-01, D-MU-03, D-MU-06, TC-10, TC-11
> architecture: StorageReportService只读方案使用原两清理页提供的有效保护快照，复用cleanupSelection锁定投影，App仅转交；原删除/废纸篓/恢复不重写，物理身份由后端核实
> verify: 临时库与卷身份/硬链接/多类别夹具、目标不足与分页边界、离线/变化、原清理入口；不使用用户片库
> review: 候选保护、空间计算和进入原清理时复核的独立审查

## P-007 图片局部与视频短样片试增强

同一模型参数试处理、同步对比、取消和明确的整图/整片入口完整连通；原模型准备、断点和资源串行保持，临时预览退出即回收。

> writes: `services/enhancement*`, `services/image_process*`, `services/media_processing*`, `app_image*`, `app_tasks*`, `app.go`, `app_settings.go`, `app_quit.go`, `app_task_center*`, `preview_asset_handler*`, `frontend/src/components/Image*`, `frontend/src/components/video-list/EnhanceDialog*`, `frontend/wailsjs/**`, `scripts/test_macos_webkit*`, `docs/context/ai-subtitles.md`, `docs/context/library.md`
> anchors: AC-08, D-MU-01, D-MU-03, D-MU-05, D-MU-08, TC-12, TC-13
> architecture: MediaProcessingService拥有会话和维护/关闭停止，EnhancementService抽取无持久状态副作用推理原语并与整片串行；样片不入库或观看tracker，临时资源绑定请求与库
> verify: 推理替身状态/取消/代次/空间/越界测试；随包runtime与合成图片/视频真实试处理；真实WebView对比；原整片超分回归
> review: 受控路由、临时文件、取消/发布竞态与原任务隔离独立审查

## Integration And Final Verification

- 重新对照八项原始需求及TC-01..13，检查图片浏览/编辑/压缩/对比与增强组合行为，无延期项。
- 新增绑定用既有固定工具链生成并规范化，执行前端bindings-usage及data-test守卫；所有App导出均有非测试消费者。
- 相关Go测试通过后按共享影响扩大，使用`/Users/zhangyukun/.local/bin/agent-go test|vet`串行执行。前端重测试同样串行并限制worker，不叠加Go、生成或构建。
- 前端完整检查和真实macOS夹具；共享服务范围需要时全量Go。原基线失败与新增回归分开记录。构建只用build-only，不安装。
- 最终diff检查复用、职责、资源上限、原文件保护、媒体可见范围和日志脱敏；对风险切片的最终集成diff做独立只读审查并修正重要问题。

## Delivery And Acceptance

八项均已实现，无延期项；用户的“仅图片严格无损”和“先另存并对比、再另行清理原文件”保持。未提交、推送、安装、部署或操作用户媒体。构建产物位于`build/bin/析微影策.app`，已完成最终本机签名。

| 原需求 | 最终行为与主要证据 |
| --- | --- |
| AC-01 / TC-01、02 | 共用视口提供适应/原始尺寸、指针锚点、拖动、空格临时放大、信息隐藏及胶片条；相邻原图最多预加载2张。视口纯函数、宿主回归及真实原生鼠标检查通过，统计口径不变。 |
| AC-02 / TC-03、04 | 查看器内裁剪/拉直/旋转/镜像及六项调整、一键改善、撤销/重做、原图对照和完整尺寸PNG副本导出。真实2048×1024源组合处理导出512×1024；另存、脏状态、释放通过。源变化、可见范围、重名/别名、取消与维护有后端保护回归。 |
| AC-03 / TC-04～06 | PNG仅重压原IDAT流，其余块原样保留，8/16位透明/文本及两帧APNG控制、时序、循环和fdAT验证通过。JPEG使用系数域优化，实际工具像素/APP/COM保真验证通过；EOI后含附加视频等数据明确拒绝。无收益不发布副本；压缩结果先对比另存，再可进入原图片清理审阅，先确认副本入库和保留。 |
| AC-04 / TC-06、07 | 双图归一化焦点联动，可解绑、恢复适应和独立处理加载失败；原生WebView同步通过。局部增强即使原图很小也等显示尺寸对比，并保留各自真实像素倍率；先红后修复及真实21项检查覆盖。 |
| AC-05 / TC-08 | 浏览、列表及相关卡片按横向指针有界取帧，明确点击时间后沿原预览定位；12帧概览按需加载且最多2路。异步合并/失活/释放及真实Blob帧/60秒定位通过；不写观看事实。 |
| AC-06 / TC-09 | 今晚候选数1～12、默认6、本机记忆，非法/损坏值不调用后端，陈旧回包丢弃；原推荐算法与过滤不变。先见7项正确失败，再与原调用方157测试通过。 |
| AC-07 / TC-10、11 | 存储体检内选卷与目标GB，方案按原清理快照保留锁，物理去重、卷隔离、收藏/评分、缺口和实际free-space分别显示；精确重复才默认勾选。临时库覆盖锁/硬链接/跨媒体别名/源变化/超过512行；交回原owner刷新复核，不提供执行旧方案直接删除接口。 |
| AC-08 / TC-12、13 | 原模型/固定2×的图片局部及≤10秒视频试片，比较后显式进入整图/原整片任务；临时会话与正式任务/观看分离。队列取消、关闭/迟到Start、源变化、工作集上限与缓冲/seek/error联动回归通过；最终签名包普通/动漫局部图片及实际视频样片通过。 |

## Verification Evidence

- **Go基线与全量**：相关原服务及App维护/退出基线通过；`agent-go test ./... -timeout 1200s`全量通过。前次失败仅任务登记表32→33的两处合同计数，已同步。其后追加JPEG附加数据保护及APNG用例，附加数据例先失败；最终`agent-go test -race ./services -run 'Test(MediaProcessing|StorageGoal|ImageEnhancementBudget|PNGOptimization|JPEGOptimization|BackgroundTaskRegistry|ProbeEnhancementRuntime)' -timeout 300s`通过。`agent-go vet ./...`通过；不把专项race写成全库race。
- **完整前端与最终局部**：完整组件241文件，3333通过、1个既有RecapPage月份断言失败（预期11，固定夹具实际10）。之后小选区等尺寸对比新增例先见正确失败，修复后7个相关文件143测试通过；未把相互重叠的数量相加。16个前端合同脚本均通过；新样式/按钮问题已修；Wails固定Go1.24.9生成并规范化，构建后bindings守卫562导出全部有消费者。
- **真实WebView**：本轮图片处理/对比/缩略图/样片夹具最终21项通过，含真实鼠标拖动、Canvas完整分辨率导出、小选区等尺寸、实际视频解码/同步及无统计副作用；最新截图`/tmp/cineinsight-image-utility-final-v2.png`已检查。原虚拟窗口、笔记、队列、字幕、区间、章节、地图及裁剪检查通过。完整入口曾因本机ENOSPC中断，空间恢复后续跑；探索夹具出现“曝光确认”和“原生拖动不打开媒体”的间歇失败，单独诊断运行17项曾通过，仍保留失败记录，**不声明完整macOS入口全绿**。探索生产交互逻辑未为此修改，仅给曝光超时追加焦点/可见性/加载诊断。
- **测试有效性**：隔离副本/Go overlay不改工作树；视口3、图片可见范围/选卷/PNG元数据/增强预算/APNG5、关闭等待/编辑裁剪/透明像素3，共11个手工变异全部被杀死，0存活、0无效。样片等待/seek/error三个用例先红后绿；JPEG附加数据和过新系统组件拒绝均先红后绿。
- **发布组件与真实执行**：`build_enhance_runtime.sh`以2 jobs完整源码重建v2，部署目标15；`build_image_tools.sh`从固定libjpeg-turbo3.2.0源码以2 jobs构建macOS15/arm64/JPEG8。打包/签名Python套件12项通过，含工具及仅依赖库过新的独立子场景，旧bundle保留。Bash语法及JPEG脚本非法参数检查通过。
- **最终产物**：`build_and_install_app.sh --build-only`成功，随后因实查发现Homebrew JPEG组件minOS27而按D-MU-09换成源码构建产物并重新执行原签名入口。最终`codesign --verify --deep --strict`通过；`otool -L`仅包内相对与系统依赖；ncnn/jpegtran/libjpeg实际minOS15、MoltenVK12。最终包内工具优先PATH、清除Vulkan/DYLD特殊环境后运行`TestMediaProcessingNativeTrials|TestJPEGOptimizationReallyPreservesPixelsAndExtraMarkers`，真实JPEG及普通/动漫图片、视频样片通过（2.994s）。合成媒体/临时库无正式任务或观看写入，原文件未变。
- **评审**：独立只读`media_utility_review`对设计、风险实现、最终集成及每项Important修复复核；清理锁来源、维护停止顺序、图片可见范围、内存、飞行Start及样片缓冲问题均关闭。JPEG附加数据与macOS15工具打包修复亦复核，无剩余Important/Critical；评审者未替主流程执行测试。
- **资料检查**：本轮本地Markdown链接及最终diff空白检查通过；Mermaid只检查源码语义，未生成渲染产物。

## Residual Limits And Recovery

- 严格无损目前为PNG/JPEG；其他格式明确不支持。动态照片JPEG附带内容不丢弃而是拒绝。编辑导出是静态8位sRGB PNG，原元数据保留在源文件；高位深/动画无损压缩与编辑导出是两种明确操作。
- 原生合成检查在当前Apple Silicon主机执行；未配置一次性PG DSN，本轮无schema变更；未在macOS15实机、完整已安装应用或TCC权限路径验收。已有Recap月份断言和探索原生交互间歇失败未扩大修复。
- 新增强运行时身份为`realesrgan-ncnn-vulkan-v0.2.0-cineinsight2`；模型/2×输出不变，旧断点按原有运行时不匹配规则拒绝续跑，未删除旧进度。模型使用现有已校验缓存，未下载权重；源码仅开发打包时拉取。
- 后续构建先准备`build/enhance-runtime`与`bash scripts/build_image_tools.sh`，再走`build_and_install_app.sh --build-only`。可重复真实原生验证位于`services/media_processing_native_test.go`，需显式设置`CINEINSIGHT_TEST_ENHANCEMENT_RUNTIME`和`CINEINSIGHT_TEST_ENHANCEMENT_MODELS`；WebView入口为`scripts/test_image_utility_webkit.sh`。不把已构建等同于已安装。
- 本机磁盘验证中一度耗尽，系统可用空间恢复后继续完成；未清理媒体、模型或Go编译缓存。已移除本轮自建旧运行时备份及Python字节码目录，保留当前构建产物和源码。
