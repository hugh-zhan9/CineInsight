# AI、字幕与场景检索

> 当前领域合同，属于 [AI-CONTEXT](../../AI-CONTEXT.md)。保留原节号；明确被取代的历史规则不再生效。

### 2.2 离线字幕生成 (Offline Subtitle Generation)
- **应用内字幕（2026-10-22，D-F01）:** 详情和队列播放器共用 `GetPlaybackSubtitle` 与 `PlaybackSubtitleControls`，有界严格读取同名SRT并转换纯文本WebVTT Blob；验证活跃视频/source_version，队列额外验证inline token。无字幕与读取失败分开，字幕失败不阻断视频。默认开，偏移±60秒/0.1秒只影响本次投影；重载只换track，不重置视频或队列token。写入成功的 `subtitle-changed` 刷新共享SRT消费者；外部修改通过「刷新字幕」重读。
- **内嵌文字轨提取（D-F06）:** 字幕工作台支持选轨→预览→确认写同名SRT；白名单subrip/srt/mov_text/ass/ssa/webvtt/text，图片轨拒绝，ASS/SSA说明样式损失。确认复核video/source/track、一次性预览token、目标和隐藏预览文件指纹，经共享规范路径锁及原备份写入器替换；最多2份/10分钟预览。提取和等待写入锁的确认均可取消，维护先Quiesce并等待；不转码视频、不调用识别/OCR。保存后索引失败沿用saved_index_pending。
集成 AI 能力实现全本地化字幕制作：
- **运行时（2026-10-12 用户裁决）:** 字幕仅使用苹果系统能力。`native/apple-ai/`（`main.swift` 分发请求，`speech_modern.swift`/`speech_legacy.swift`/`translation.swift` 分别是 macOS 26+ 识别、macOS 15 识别与翻译，`speech_words.swift` 是 macOS 15 结果累积、词时间与检测判定的纯逻辑，`protocol.swift` 是 JSON 协议类型）通过随包的 `CineInsightAppleAI.app` 提供系统识别、自动语言建议、翻译和资源准备；`services/apple_ai.go` 管理有界 JSON 子进程调用、取消与时间片段转换。macOS 15 以下或组件缺失明确不可用，不回退第三方 AI。2026-10-13 用户裁决「全部支持」（修订 D-004）：macOS 26+ 用 SpeechAnalyzer 与 `TranslationSession(installedSource:target:)`；macOS 15 用 SFSpeechRecognizer 仅本机识别（≤50 秒分段；打开中间结果并累积，因为本机识别停顿后会重开转写、最终结果只剩最后一句，实测只取最终结果会丢掉每段除最后一句外的全部内容；需语音识别授权，`supportsOnDeviceRecognition` 为 false 时报告 `apple_speech_dictation_missing`，它表示当前本机识别不可用，不能据此确定资源未下载；macOS 15 的准备动作只检查权限和能力，界面引导核对系统听写设置与系统提示，不承诺添加语言或实际听写一次就能获得离线能力，拒绝/60 秒未答报 `apple_speech_permission_denied`，`status` 遇拒绝/受限授权返回 supported:false + 该码 + 完整语种列表，界面显示设置说明且翻译语言照常可用；只有「未检测到语音」(kAFAssistantErrorDomain 1110) 跳过该段，其他识别错误整次失败；识别器不报置信度时自动检测不采用结果、要求手选，macOS 15 上自动检测可能经常需要手选（本机 macOS 27 强制旧路径时句末结果带置信度）；重新开始按实测形态判定：无时间的中间结果从不单独记下，带时间的句末结果之后出现无时间结果即新句，两个带时间结果之间看时间；纯逻辑测试 `scripts/test_apple_ai_logic.sh`（含真实快照回放夹具），`go test ./...` 经 `TestAppleAISwiftLogic` 运行它；字幕与人脸状态原因只给中文说明，不带错误码）和不可见窗口里的 `translationTask`（先查语言对已安装）。该路径与准备翻译一样运行 NSApplication，必须在 Swift 主函数第一次挂起前进入，否则会话永不开始。`CINEINSIGHT_APPLE_AI_LEGACY=1` 在新系统上强制走 macOS 15 路径，仅供测试。设计见 [系统字幕概要](../../docs/loopx/design/2026-10-12-apple-subtitles/概要设计.md)。
- **流程:** 视频 -> FFmpeg (提取音频) -> Apple Speech（系统语音识别） -> 写同目录隐藏临时文件（pending）-> 后处理校验 -> 经字幕写入器替换同名 .srt。校验通过前原 `.srt` 一个字节都不动；空结果与取消都删掉临时文件（D-PC13，2026-09-29/30）。
- **字幕写入器（D-PC13）:** 所有覆盖用户 `.srt` 的路径（生成、强制生成、翻译、工作台保存、编码转换、清理合并、超分产物的字幕复制与内嵌文字轨提取确认）都走 `services/subtitle_file_writer.go` 的 `SubtitleFileWriter.Replace`：同目录临时文件 → fsync → 目标已存在时先备份到 `~/.CineInsight/subtitle-backups/<videoID>/<unixNano>.srt`（每个视频只留最新 5 份）→ 原子替换 → 恢复原文件权限。行菜单「恢复上一版字幕…」调 `ListSubtitleBackups` / `RestoreSubtitleBackup`，恢复前同样先备份当前文件。生成前 `GetSubtitleOverwriteInfo` 告诉前端是否会覆盖、以及同目录同基本名的其他视频是否共用这份 `.srt`，确认文案写明「将覆盖现有字幕（会先备份，可恢复）」。替换失败时保留 pending 并返回 `subtitle_replace_failed`。
- **抗幻觉:** 当前仍保留基于后处理的质量校验与强制生成分支。
- **幻觉确认（2026-09-29/30 改为可追溯，D-PC20）:** 校验未通过的任务进入 `needs_confirmation`，临时文件登记在任务行上；前台弹窗与任务中心都能处理：强制生成（`ForceGenerateSubtitle` 直接复用临时文件）、放弃（删临时文件）或重试。此前只靠当场弹窗询问，弹窗关掉结果就找不回（MEDIA-04）。
- **任务取消:** 字幕生成过程中可随时取消 (`CancelSubtitle`)，通过 `exec.CommandContext` 终止子进程。
- **双语字幕 (可选):** Apple Translation 翻译原文，再合并为双语 SRT（原文上行、翻译下行）；翻译失败仍保留原文并返回警告。资源缺失可在字幕设置选择源/目标语言，由系统界面请求下载；正常翻译在后台复用已安装资源。
- **手动翻译已有字幕（2026-09-13 裁决）:** 片库行菜单「翻译字幕…」对视频同目录已有的 `.srt` 单独发起翻译，目标语言与输出形态（仅译文 / 双语对照）每次指定；不碰 ASR，也不排字幕生成队列。产物**覆盖同名 `.srt`**——用户裁决不新建 `名称.zh.srt` 这类多语言文件，以免动到字幕索引、重命名跟随、文件迁移与 Jellyfin 暴露这一串既有消费者。2026-09-29/30 起覆盖经字幕写入器进行，原文件先进备份、可从行菜单恢复（D-PC13，MEDIA-05）；翻译弹窗可「后台继续」，进度显示在行菜单标签、片库页进度条与任务中心，翻译期间不再锁住整个应用（D-PC22，MEDIA-12）。非 UTF-8 的字幕（GB18030 / Big5 等）在翻译与工作台入口返回 `subtitle_encoding_not_utf8`、不解析不写入，用户可先「转换为 UTF-8」（`ConvertSubtitleToUTF8`，按所选编码解码后经写入器写回；GB18030 与 Big5 都能干净解码时返回 `subtitle_encoding_ambiguous` 并附预览让用户选，D-PC14）；只有内嵌或其他格式字幕、没有同名 `.srt` 时返回 `subtitle_not_sidecar_srt`（D-PC17）。翻译先落临时文件，翻译或写回任一步失败都保留原字幕；双语合并后会把文件权限恢复成翻译前的值（`mergeBilingualSRT` 固定写 0644 是给新生成字幕用的，不该放宽用户已有文件的权限）。翻译期间可取消（`CancelSubtitleTranslation` 按 videoID 登记 cancel func），取消后原字幕不动。**空译文保留原文**：翻译器可能对拟声词、音乐符号、纯标点行返回空串，写出的空块会被 `parseSRTEntries` 与编辑器解析器整条丢弃——仅译文模式下等于永久删除该条字幕，双语模式下会让其后所有译文错位；该修复在 `translateSRTWithProgress` 内，生成流程的自动双语同样受益。回退发生在喂上文滑窗之前（否则「原文 → 空」会作为示范样本进入下一批提示词），回退后的条目在双语合并时只写一行（`mergeBilingualSRT` 遇到译文与原文相同就不再重复），回退条数会作为 warning 回报给用户——否则「一条都没翻成」和「全部翻成了」是同一个成功提示；这条 warning 两条路径共用 `subtitleFallbackWarning`，生成流程的自动双语同样回报。空译文回退已抽成 `applyTranslationFallback`，生成、文件翻译与工作台重译三处共用（D-PC16）。取消只能在两批之间或请求飞行中生效；`lockSubtitleFile` 的同步入口不传 ctx，所以「排队等锁」期间点取消会记账、但要等拿到锁才返回（拿到锁先复查 ctx，不会再发翻译请求，也不会改文件）。字幕设置只保留系统语言资源准备与自动双语开关，两个入口共用系统翻译。前端对「多数条目已是多行」的字幕给出叠加警告（覆盖语义下重复翻译会在译文上继续叠加）。
- **语言选择（2026-10-12）:** 手选系统支持的语种，或用视频开头至多 30 秒音频在已安装语言中进行候选转写，结合转写置信度和 NaturalLanguage 的文字语种一致性推测。界面展示检测范围与可修改的结果，记住上次选择；静音、短到无法判断、置信度不足或接近时明确要求手选。不是苹果原生的全语种音频语言检测。语言资源由用户显式准备，不为了检测自动安装未准备语种。
- **任务队列:** 字幕生成通过 FIFO 队列串行执行，前端显示当前任务和排队任务，支持逐任务取消、后台继续和队列状态事件。
- **队列持久化（2026-09-29/30，D-PC20）:** 每个任务对应一行 `subtitle_jobs`（队列任务 ID 就是行 ID，进度事件与 `CancelSubtitleTask` 都用它）：入队写 `queued`，开始时条件更新为 `running`，结束写终态；终态行只保留最近 100 条，`interrupted` 与 `needs_confirmation` 不参与裁剪。启动时把残留的 `running` / `queued` 改为 `interrupted`，片库页提示「上次中断 N 个字幕任务」，可「全部重新排队」（`RequeueInterruptedSubtitleJobs`）或「忽略」（`DismissInterruptedSubtitleJobs`，改为 `cancelled`）。`ResolveSubtitleJob(jobID, force|discard|retry)` 只负责入队、立即返回；并发操作输的一方返回 `subtitle_job_conflict`。失败时发 `subtitle-failed`（可带 `error_code`），系统通知文案指向「任务中心」。退出应用前有排队或在跑的字幕任务会先弹退出确认（见 2.19）。
- **本机识别资源缺口补装（2026-11-04）:** macOS 15 没有给第三方任何下载识别模型的接口，而系统只为已注册成听写语言的语言下载 `com.apple.speech.asr.transcription.<lang>`（资产类型 `com.apple.MobileAsset.UAF.Speech.AutomaticSpeechRecognition`）；听写语言跟随 `com.apple.HIToolbox` 启用的输入源。`scripts/install_speech_assets.sh`（`--status` / `--dry-run` / `--languages` / `--timeout` / `--helper`，纯逻辑在 `scripts/speech_assets.py`）临时追加对应输入源让系统发起下载，等 `status` 报 installed 后把系统偏好原样还原，默认 ja,ko,en,zh，目前只有 ja（Kotoeri 罗马字）与 ko（2-Set Korean）能自动加输入源。macOS 15.7.2 实测：ko、ja 下载后 `prepare_speech` 返回 `supported`、`transcribe` 正确转写，还原输入源后仍 installed。应用侧不改：仍只本机识别，缺失时仍报 `apple_speech_dictation_missing`，音颈不出本机。
- **强制生成复用:** 质量校验失败的 SRT 以隐藏临时文件暂存，路径记在任务行的 `pending_artifact_path`；用户选择强制生成时直接复用该文件，不重复执行识别，重启后同样可用。 历史旧引擎 pending 仍能强制完成；旧任务重试时持久化改为 Apple 引擎，避免后续强制完成错用旧身份。
- **引擎准备与无字幕同步（D-PC22/23）:** 系统语言准备/交互式语言检测同一时间只允许一轮，可取消（`CancelSubtitleEnginePreparation`）；设置页「字幕识别质量」分区显示系统引擎与语言资源状态。「无字幕」视图、字幕搜索、按筛选随机与代理入队等依赖字幕索引的调用，距上次全库同步不足 10 分钟时直接用缓存，否则在后台同步一轮并发 `subtitle-index-synced`；视图上显示「上次同步时间 · 立即同步」（`GetSubtitleIndexSyncStatus` / `SyncSubtitleIndexNow`）。批量操作栏可「批量生成字幕」。 应用退出会取消并等待原生准备子进程，退出后拒绝新准备。
- **字幕搜索筛选:** 字幕关键词搜索会把标签、文件大小和视频高度筛选下推到数据库查询，避免先取回全量结果再由前端过滤。
- **字幕命中跳转:** 字幕搜索结果保留首个命中片段的起止时间并复用主列表虚拟壳；点击命中或预览时，支持内嵌预览的格式会在媒体元数据就绪后跳到命中时间，普通预览不会复用旧跳转位置。
- **重命名联动:** 重命名视频会同步重命名同目录 `.srt` 文件，并刷新字幕索引。
- **依赖管理:** FFmpeg 保留现有本地/Homebrew 查找与 macOS 安装路径；字幕 WhisperX/Qwen 执行、安装、预下载脚本和设置入口移除。已下载的旧模型缓存不自动删除。原来放在 WhisperX 文件中的 Python 公共工具移到 `services/python_runtime.go`，继续服务中文文字场景检索。
- **术语表退役（2026-10-12 用户裁决）:** 全局和作品集术语表入口、Wails 管理绑定及翻译注入移除；`translation_glossary_entries` 和历史数据保留。系统翻译不承诺指定术语译法或旧 LLM 滑动窗口上下文。旧字幕提供方/API Key/WhisperX 设置字段保留在历史数据库中但不再通过 JSON 返回、不用于执行。

### 2.13 字幕编辑工作台
- **范围:** 只编辑视频同名外置 `.srt`；ASS/SSA/VTT 和内嵌字幕不提供编辑入口。
- **严格解析:** `subtitleparser` 的编辑用 codec 与检索用宽松解析分离，坏块不静默跳过而是报错，避免编辑时丢内容。2026-09-29/30 起（D-PC15，MEDIA-06）`end <= start` 从加载错误降为 issue：文档带 `issues [{index, kind: zero_duration|end_before_start|overlap}]`，WhisperX 的零时长条目能打开；保存前仍拒绝有 issue 的文档，但返回第一个问题的位置，前端提供「下一个问题」与「一键修复」（零时长：结束 = 开始 + 500ms 且不越过下一条；重叠：前一条结束 = 后一条开始 − 1ms）。没有同名 `.srt` 时返回 `subtitle_missing`，可「新建空白字幕」（`CreateBlankSubtitleDocument`），保存时创建文件。非 UTF-8 文件返回 `subtitle_encoding_not_utf8`（见 2.2）。
- **编辑能力:** 视频同步预览与点击定位、逐条改文本与起止时间、插入/删除/拆分/合并、全局或选区时间平移、查找替换、选区重译、100 步会话级撤销重做。选区重译有两种形态（D-PC16，MEDIA-07）：`translation_line` 只把每条第一行送去翻译、只替换第二行（选区多数条目为双语两行时默认），`whole_entry` 整条重译。
- **冲突检测:** 打开文档时记录 `size + mtime + sha256` 指纹，保存前二次校验；外部已修改时返回 `subtitle_conflict` 并拒绝写入一个字节。
- **原子保存:** 覆盖当前文件；Unix 用 `rename`，Windows 用 `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)`。替换失败时原文件保持完整可读。~~不保留历史或 `.bak`~~——已被 2026-09-29/30 产品完善度批次推翻：保存经字幕写入器进行，覆盖前先备份到应用数据目录（每视频最近 5 份，不在媒体目录留 `.bak`），可从行菜单恢复（D-PC13，见 2.2）。
- **写锁:** 保存与字幕生成共享同一把视频级串行写锁，避免应用内生成与编辑互相覆盖。
- **保存状态:** `saved` / `saved_index_pending`（文件已写入但字幕索引刷新失败）/ `rejected`，不把部分成功伪装成成功。
- **未保存修改（2026-10-14 G1）:** 只在编辑区开着时（不在读取中、不在状态页）才算「有未保存修改」：读取中、没有字幕、编码待转换、只有内嵌字幕、读取失败这些状态页以前标题栏写着「有未保存修改」（标记只在读到字幕时设），关闭、刷新都要确认丢弃并不存在的修改；丢弃修改后刷新而文件读不出来时同样。`SubtitleWorkbench.vue` 的 `editorOpen` / `isDirty`。
- **⌘Z / ⇧⌘Z 的范围（2026-10-14）:** 字幕正文框（每次输入都经 `updateEntryText` 进撤销记录）里与焦点不在输入框时是工作台撤销、重做；查找、替换、偏移与条目时间框里打的字不进记录，⌘Z 交给浏览器撤销框里的字（`isTextEntryTarget` 且不是条目正文框时不拦），以前会悄悄撤掉别处的一条字幕。条目时间框按「已确认」区分：框里的字与 `data-committed`（已记进记录的值）一致时 ⌘Z 是工作台撤销（改完回车后撤回这次时间修改），还在打字时撤框里的字。

### 2.16 AI 质量评估
- **数据来源:** 纯本地统计，不额外调用模型、不训练、不自动调整提示词或阈值。
- **运行归因:** `ai_tagging_runs` 每次实际 AI 打标任务一条，记录状态、模型标识、提示词版本、耗时、请求数和工具调用数；`ai_tag_candidates.run_id` 关联当前 run，历史候选保持 NULL。进程退出留下的 `processing` 在下次启动改判 `failed/interrupted`，不自动重跑。
- **同源评估历史:** `ai_same_source_evaluations` 保存实际展示给用户的关系样本，相同指纹的重复任务只刷新关系不增加样本；用户点"不是同源"时在同一事务内更新 relation 与当前 evaluation。
- **指标语义:** 标签窗口用 `COALESCE(approved_at, rejected_at)`，同源用 `COALESCE(rejected_at, detected_at)`，运行用 `completed_at`。`pending` 和 `superseded` 永不进入分母；所有比例同时返回分子、分母和浮点率，分母为零时率为 `null` 而不是 0%。
- **历史未知版本:** 缺少模型或提示词版本的旧记录统一映射为 `historical_unknown`，显示"历史/未知版本"，不推测为当前模型。
- **入口:** AI 标签管理拆成"待审工作台"和"质量评估"两个 tab，默认 30 天窗口，支持 all/30d/7d 与标签、置信度、模型、版本过滤。质量视图只读，不提供批准、拒绝、重跑或阈值修改。
- **隐私:** 只持久化计数、耗时、稳定版本号和模型名称；不保存帧、字幕正文、请求/响应 body、API Key、base URL 或本地路径。
- **软删除:** 软删除视频的历史样本仍进入聚合，但不返回可操作记录。

### 2.32 字幕与画面场景检索（2026-10-10）
- **对白:** 复用既有`subtitle_segments`（同名`.srt`逐条索引与10分钟节流同步），`LOWER(text) LIKE`字面子串（转义`%`/`_`/`\`），返回全部命中条目的起止时间与前后一条上下文；不另建第二份字幕事实。只有其他格式或内嵌字幕的视频计入覆盖率“未索引”，可先用既有字幕生成。
- **画面（本地，默认）:** `SceneRuntime`（`~/.CineInsight/scene-runtime/{python,venv,models/cn-clip-vit-b16-q8,scene_worker.py}`，身份`scene-cn-clip-vit-b16-q8@1-onnxruntime-1.23.2`），只用于中文文字搜画面：托管Python 3.10、pin `onnxruntime==1.23.2 numpy==2.2.6 pillow==12.3.0 tokenizers==0.23.3`，模型为Xenova Chinese-CLIP ViT-B/16量化ONNX（固定提交、三文件sha256校验，`scene_model_mirror_url`替换主机）。`PrepareSceneRuntime`显式触发、可取消，事件`scene-runtime-state`；健康面板只读缓存状态。本机冷准备约48秒，检索约40ms。
- **索引:** `StartSceneIndex`（视频ID或LibraryFilter，服务端解析为活跃、非失效、在范围内），单worker、每项取共享`MediaWorkSlot`、登记表键`scene_index`、事件`scene-index-state`，不进空闲门也不阻止退出。ffmpeg按`scene_visual_interval_seconds`（默认5，2–30；无gorm default，新库显式行+迁移补默认）抽224×224帧，相邻帧余弦≥0.92合并成段，向量int8量化（4字节scale+512字节）存`scene_visual_segments`，状态在`scene_index_states`（模型、源size/mtime、间隔）；源变化中途丢弃。查询只用当前模型、`state.source_size = videos.size`且活跃非失效的段；“清理旧索引”保留当前文字模型和苹果模型，互不误删。
- **外部（可选）:** `scene_visual_provider=external`须在设置页确认披露后保存，复用AI打标的OpenAI兼容视觉模型为每8帧生成中文短描述（`external-caption:<模型>@1`，无向量，字面检索）。本地不可用/失败只报告`scene_runtime_unavailable`等notice，绝不改用外部；外部失败也不回退本地。
- **查询与入口:** `SearchScenes({query, mode: all|dialogue|visual, filter, limit≤200})`，`all`用倒数排名融合（k=60）并保留来源；返回`hits/coverage/notices`。顶栏工具组“场景检索”页、片库工具栏“在当前筛选中搜场景”、设置页“场景检索”分区；命中缩略图走`/preview/frame/{id}?ms=&w=`（`ThumbnailService.RenderFrameAt`，精确定位单帧JPEG、内存LRU 64帧、10秒期限、按源size/mtime失效，视频工作台共用），点击打开详情抽屉并定位到起点前2秒。合同见[场景检索合同](../../docs/loopx/design/2026-10-10-media-workbench/场景检索合同.md)。
- **加载更多与截断（2026-10-14）:** `SearchScenes` / `SearchScenesByImage` 结果加性新增 `truncated`（符合的命中多于这次的 limit）。场景检索页每次 50 条，`truncated` 时给「加载更多」（同一查询 limit 加 50 重查，最多 200，新结果到之前旧结果留在屏上）；到 200 仍截断时写「只显示前 N 条，换个更具体的说法缩小范围。」（以图：「只显示最相似的前 N 条。」，N 是合并相邻命中后的实际条数）；截断时每部视频的计数写「已列出 N 处」，不冒充全部。

- **以图搜片（2026-10-12）:** `SearchScenesByImage({image_base64 | video_id+at_ms, filter, limit})`（`services/scene_image_search.go`，App 同名）。查询图二选一：上传的图（JPEG/PNG/WebP/GIF/BMP，解码后≤20 MB，可带 `data:` 前缀，`http.DetectContentType` 判型，否则 `scene_image_invalid`），或某部片某一刻的画面（App 把 `ThumbnailService.RenderFrameAt` 480px 接成取帧函数，与命中缩略图同一实现与缓存）。图片临时落盘交给 Apple Vision revision 2 的768维图像特征，用完即删、不写日志。苹果索引与中文文字索引独立：仍由同一 `SceneIndexService` 管理，`StartSceneIndex(provider=apple)` 不需要 CLIP；长边640等比抽帧、每帧成段、特征以float32存共享段表（独立model_id），状态在 `scene_image_index_states`。查询按欧氏距离、相邻段合并、范围和来源±10秒排除；设置里的外部文字提供方不影响以图查询。覆盖率 `image_indexed/image_available` 分别报告，未索引时提示建立苹果索引；系统不可用/提取失败明确提示，不调用第三方。入口：场景检索页「以图搜片」选文件、把图拖进本页、本页可见且有图片时接管粘贴；每条命中旁「相似画面」；详情抽屉「找相似画面」取当前播放位置，经片库页 `find-similar-scenes` → App `openSimilarScenes` 带 `scopeRequest.similar` 进场景检索页直接检索。

- **台词卡（2026-10-12）:** `QuoteCardDialog.vue` + `utils/quoteCard.js`：取 `/preview/frame/{id}?ms=&w=960` 单帧，在 canvas 上合成「字幕条」或「海报」两种版式（长边放大到 1920，台词最多 120 字，字幕条 4 行、海报 6 行，超出以「…」截断；中文逐字、英文按词折行，行首不放逗号句号），画面读取失败时只报错并禁用导出。「复制图片」走 `ClipboardItem`（交给它生成 PNG 的 Promise，保证在点击里同步发起），失败显示原因、不悄悄改走保存；「保存为 PNG」调 `SaveQuoteCard(pngBase64, suggestedName)`（`app_quote_card.go`：先校验再弹原生保存框，≤30 MB 且 PNG 头有效，同目录临时文件再改名，取消返回空串，日志不记台词与路径）。入口：场景检索页对白命中「台词卡」（取字幕区间正中那一刻）、详情抽屉「台词卡」（当前播放位置 + `GetSubtitleSegments` 找当前字幕，没有则台词留空）。

### 字幕显示、台词与编辑宿主（2026-11-03）

详情与队列的内嵌播放器共用 `PlaybackSubtitleControls`：字号、背景、位置保存为本机显示偏好，时间偏移仅影响当前播放投影，不改 SRT。`PlaybackTranscript` 用同一 cue 投影和虚拟窗口显示台词；手动滚动暂停自动跟随，点击只定位到当前合法播放区间。换源、队列会话和字幕刷新使旧读取失效。

编辑/生成先受控退出全屏，再经 App 导航到常驻 `VideoListPage` 宿主。相同视频复用编辑器，换视频先走未保存确认再重新挂载；异步读取、导航确认和依赖检查都受请求代次保护，离页后的旧结果不弹窗。字幕工作台不再随播放器换源销毁。真实 WKWebView 验证入口为 `scripts/test_playback_subtitles_webkit.sh`，已加入串行 macOS 验收脚本。完整范围与证据见[实施记录](../loopx/plans/2026-11-03-library-experience.md)。

### 可选外部文本用途与组合搜索（2026-11-03）

`query_parse_enabled`、`chapter_summary_enabled` 两个用途独立默认关闭；复用既有 AI 地址/模型配置，保存文本用途开关本身不触发打标。查询调用前展示服务、模型及发送文字，模型只接收查询文本与固定指令。`TextAIService` 限制响应1MiB、查询20秒，禁用重定向，不重试、不调用工具；修改配置、关闭用途和维护都会使旧授权失效并取消请求。状态读取仅判断配置，不声称服务探测成功。

本地/外部解析都产生待核对条件，人物重名需选择，出演/导演语义需明确接受按“人物关联”筛选。模型输出不能自带SQL、ID或工具；已知条件漏项/反转拒绝，无法核对的片段保留为待处理项，不能静默丢掉。候选仅在本地解析，SQL跟踪不记录这些实体名。按请求ID取消贯通窗口与HTTP，取消先到达也不会再外发；新请求不受旧取消影响。已发送文字不能撤回。解析草稿10分钟内有效，不进数据库备份，关闭/切库后旧授权不再可用；明确条件可应用到原片库并通过原命名视图保存。章节摘要复用文本适配器，但用途授权与取消代次彼此独立，详见下节章节合同。

### 搜索汇总与受控预览（2026-11-04）

观看导航及命令面板新增「搜索汇总」。视频、人物、作品集使用既有分页；各组独立加载和显示错误。对白以具体 video_id 升序分页（默认20、最多50部），每片首取3条，以 start_ms/id 继续读取（默认30、最多200条）。命中计数与当前页在同一读取快照中计算；跨页片库可变化，前端按ID去重。游标用服务实例密钥签名，绑定规范化查询/筛选、视频范围和数据库实例，10分钟过期；失效须明确刷新，不自动返回首页。

对白刷新复用原SRT索引；每页最多核对1000个候选，剔除旧命中后继续补位，最终页只读取已校验候选。达到扫描界限仍给出继续游标，不把未核验的后续视频补入结果。全库旁挂字幕发现仍由既有节流同步负责。画面/以图最多返回相关前200段并标示 ranked_top_k，不声称全量命中数；未执行明确显示能力缺失。新结果取得时携带原片版本，画面版本来自排名前捕获的索引来源；未知或已变来源不能借点击时的新指纹授权旧位置。

专业场景页保留原点击定位及台词卡行为，新增「按影片分组与预览」，复用汇总页的分组组件。单条和本片已加载命中都经过精确确认再追加到区间队列；对白前后各2秒并夹在片长内，画面沿原段区间，不隐式加载其余命中。临时预览不创建书签、不记正式观看；「保存到专题」复用书签与专题单事务。返回保留查询、展开和结果，数据事件仅提示刷新；图片只在会话内保存。新查询、失活或卸载撤销未提交预览与迟到token，已提交操作按原结果收尾。

搜索相关的筛选、人物/作品集查询、场景描述查询及字幕索引正文写入使用局部静默SQL会话，避免调试或慢SQL跟踪带出查询、字幕正文；全局日志与原事务维护闸门保持。

### 原文件章节、人工章节与字幕摘要（2026-11-04）

MediaProbe 请求 ffprobe 的 chapters，与既有技术快照一起保存。NULL 表示尚未读取，`[]` 表示已读且无章节；坏的章节投影单独标 invalid，不影响有效时长、编码和音轨。原章节不套人工标题长度、500章和非重叠规则；复制进人工编辑稿后再按人工规则校验。重新探测不覆盖人工章节。

ChapterService 拥有人工集合、草稿和请求幂等记录。人工集合按 video_id 唯一及父 revision CAS 保存，允许间隙但不允许重叠，时间为毫秒且不能超出已知片长；空数组必须明确 clear。章节是逻辑媒体引用，原片删除后仍能从观看笔记的「章节」阅读。源变化不接受旧定位，用户核对后才能显式用新源保存。

摘要必须先准备并展示具体视频、提供方/模型、字幕完整边界、条数、字数与分段数，再确认启动；不自动 ASR、提取或外发媒体。输入复用严格 SRT parser 和包含内容 SHA 的指纹；限定所选范围不超过100000字/20000条，每段不拆cue且最多8000字，同时限制传输字节；不截断尾段、不自动重试。模型只能给标题、摘要及现有cue编号，时间在本地映射，伪造、乱序、重复和重叠证据拒绝。只持久草稿输出和证据位置，不保存完整字幕输入。每请求60秒、任务从queued起15分钟（含排队）；超时failed/summary_timeout，与用户取消区分。

请求 UUID 与请求摘要唯一绑定，queued 提交后才允许调用模型。相同请求只返回原 job，改变内容复用 ID 拒绝；重启遗留 queued/running 转 interrupted，不自动再次付费。最近20个无正文终态之外压缩内容/视频引用/诊断，保留 request_id 和摘要占位，备份/迁库后仍不能重复外发。

查询与摘要拥有独立 TextAI grant：查询仍用 status.provider_revision，摘要准备使用 status.summary_revision。独立开关仅撤销对应用途，共享服务/模型/密钥、切库和停止才同时撤销。关闭或维护取消正在运行/排队的摘要，不能撤回已发送文字。draft_ready 的最后复核与写入保持在路径/SRT协调区间，并由用途grant守住短DB提交；不在该区间调用模型。取消后的晚响应不落库。

应用先预览精确新增、删除和保留范围（10分钟）；局部范围只替换完整位于范围内的章节，跨边界明确列出冲突，不拆分、截断或删除。范围外章节ID、文字和位置不变。应用也在路径/SRT协调内复核并提交，job/人工父revision条件更新同事务，无新增显式DB锁。关闭未提交预览撤销token，提交回执可重取。展开摘要无观看副作用，章节定位复用原详情播放器并携原片版本，生成和手工保存都不改写视频容器。

### macOS 15 本机识别提示澄清（2026-11-04）

用户明确选择“核实并修正文案，保持本地识别”。保留原错误码和 `requiresOnDeviceRecognition=true`；只修正错误说明、识别语言标签及准备成功提示。自动检测仍只使用当前本机可用语种，手动选择不会绕过这一要求；不增加在线兜底，不执行识别守护进程重置。

依据：[Apple supportsOnDeviceRecognition](https://developer.apple.com/documentation/speech/sfspeechrecognizer/supportsondevicerecognition) 只定义能否无网络识别，不提供资源下载状态；[Apple 听写说明](https://support.apple.com/en-gb/guide/mac-help/mh40584/mac)要求在系统设置检查是否需要网络。当前验证环境为macOS 27.2，用户粘贴的15.7.2探针数值未在该旧系统复测。
