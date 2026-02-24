# Video Master 代码审查记录（2026-02-24）

## 1. 审查背景
本次审查聚焦于 2026-02-24 新增/修改的功能：
1. 字幕服务升级：多语言模型、抗幻觉参数、SRT 后处理校验。
2. 双语字幕翻译：DeepL API 集成、SRT 解析/翻译/合并。
3. 标签管理优化：颜色自动分配、软删除恢复。
4. 设置模型扩展：3 个新字段及前端 UI。

---

## 2. 审查范围
- 后端：`app.go`、`models/video.go`、`services/subtitle_service.go`、`services/tag_service.go`、`services/settings_service.go`
- 前端：`SettingsPage.vue`、`TagManagerDialog.vue`、`AddTagDialog.vue`、`VideoListPage.vue`
- 文档：`AI-CONTEXT.md`、`docs/design-subtitle.md`、`docs/req-subtitle-plan.md`

---

## 3. 主要问题清单（按严重级别排序）

### S1-高：DeepL API Key 明文存储在 SQLite 中
- **位置**：`models/video.go:48`、`services/settings_service.go:32`
- **现象**：`DeepLApiKey` 以明文字符串存入 SQLite 数据库，无加密处理。
- **影响**：
  1. 数据库文件 `~/.video-master/video-master.db` 直接可读，API Key 泄露风险高。
  2. 若用户备份或分享数据库，Key 不可撤回。
- **建议**：使用系统密钥链（macOS Keychain / Windows Credential Store）存储敏感信息；或至少在存储前做对称加密（如 AES + 硬件指纹派生密钥）。

### ~~S1-高：DeepL HTTP 请求无超时控制~~ ✅ 已修复
- **位置**：`services/subtitle_service.go:455`
- ~~**现象**：使用 `http.DefaultClient.Do(req)` 发送 DeepL 翻译请求，未设置任何超时。~~
- ~~**影响**：~~
  1. ~~DeepL 服务端响应缓慢时整个字幕生成流程将无限阻塞。~~
  2. ~~用户无法取消或感知卡顿原因，只能看到"Translating via DeepL..."不动。~~
- ~~**建议**：创建带超时的 `http.Client`（如 `&http.Client{Timeout: 30 * time.Second}`），或使用 `context.WithTimeout` 传递可取消的上下文。~~
- **修复**：新增 `deeplHTTPClient = &http.Client{Timeout: 30 * time.Second}` 替代 `http.DefaultClient`。

### S2-中：`GenerateSubtitle` 使用 `goto done` 控制流
- **位置**：`services/subtitle_service.go:189`
- **现象**：翻译失败时使用 `goto done` 跳转到函数末尾完成收尾工作。
- **影响**：
  1. Go 中 `goto` 可读性较差，后续维护者可能遗漏或误改跳转目标。
  2. 如果在 `goto` 和 `done:` 之间新增代码，可能引入微妙的 bug。
- **建议**：将双语翻译逻辑提取为独立方法（如 `processBilingualTranslation`），失败时直接 `return`，由调用方在翻译后继续执行收尾逻辑。

### ~~S2-中：`parseSRTEntries` 正则表达式每次调用都重复编译~~ ✅ 已修复
- **位置**：`services/subtitle_service.go:395`
- ~~**现象**：`regexp.MustCompile(\r?\n\r?\n)` 在每次调用 `parseSRTEntries` 时都编译一次。~~
- ~~**影响**：字幕条目多时（如 500+ 条），每次翻译/合并操作会重复编译正则。性能影响微小但属于不良实践。~~
- ~~**建议**：提取为包级 `var srtBlockSplitter = regexp.MustCompile(...)` 复用。~~
- **修复**：提取为包级变量 `srtBlockSplitter`，`parseSRTEntries` 直接复用。

### ~~S2-中：`transcribeCLIWithLang` 语言检测正则可能匹配失败~~ ✅ 已修复
- **位置**：`services/subtitle_service.go:286-290`
- ~~**现象**：正则 `auto-detected language:\s*(\w+)` 假设 whisper 输出固定格式。不同版本的 `whisper-cli` 输出格式可能变化（如 `whisper.cpp` v1.7 改为 `auto-detected language: xx (p = 0.95)`）。~~
- ~~**影响**：未匹配到时默认 `en`，可能导致本不该翻译的英文视频仍送 DeepL 翻译（浪费 API 额度），或中文视频误判为英文。~~
- ~~**建议**：增加 fallback 日志告警；或同时匹配 `language: (\w+)` 等多种格式；考虑从 whisper 的 `--print-progress` 或 `--output-json` 获取更结构化的语言信息。~~
- **修复**：预编译 `langDetectRe` + `langDetectReFallback` 两个正则，未匹配时打印 WARNING 日志。

### S2-中：`DeepLApiKey` 可能在日志中泄露
- **位置**：`app.go:297`
- **现象**：日志行 `log.Printf("API GenerateSubtitle id=%d path=%s bilingual=%v lang=%s", ...)` 虽然没有直接打印 API Key，但如果未来有人在调试时添加打印 settings 内容的日志，Key 会泄露。
- **影响**：间接泄露风险。当前不直接暴露，但 `settings` 对象包含敏感字段。
- **建议**：在 `Settings` 模型上实现自定义 `String()`/`GoString()` 方法，脱敏 `DeepLApiKey` 字段（如只显示前4位+****）。

### S3-低：`isSameLanguage` 语言映射表不完整
- **位置**：`services/subtitle_service.go:365-380`
- **现象**：`langMap` 仅映射了 10 种语言，但 whisper 支持 99 种语言。不在映射中的语言（如 arabic, hindi, turkish）将直接用 whisper 输出的全名与用户设定的 2 字母代码比较，永远不匹配。
- **影响**：对映射表外的语言，即使检测语言和目标相同也会多走一次 DeepL 翻译。
- **建议**：使用更完整的映射（参考 whisper.cpp 源码中的 `whisper_lang_str` 表），或让 whisper 直接输出 ISO 639-1 代码（`-l` 参数本身使用的就是短代码）。

### S3-低：设置页 `select-input` 和 `text-input` CSS 类未定义
- **位置**：`frontend/src/components/SettingsPage.vue:83`、`SettingsPage.vue:103`
- **现象**：`class="select-input"` 和 `class="text-input"` 未在 `App.vue` 全局样式或 `SettingsPage.vue` 中定义。
- **影响**：下拉框和文本框使用浏览器默认样式，视觉上与其他输入框不一致。
- **建议**：在 `App.vue` 中添加 `.select-input` 和 `.text-input` 样式定义，保持与现有 `.number-input` 风格一致。

### S3-低：`App.vue` 中 `settings` 初始对象缺少新字段
- **位置**：`frontend/src/App.vue:54-61`
- **现象**：`data()` 中的 `settings` 默认值不含 `bilingual_enabled`、`bilingual_lang`、`deepl_api_key`。虽然 `loadSettings()` 会从后端加载覆盖，但在加载完成前一瞬间 `SettingsPage` 如果被渲染，这些字段为 `undefined`。
- **影响**：极端情况下可能导致 Vue 响应式检测不到新字段。
- **建议**：在默认 `settings` 对象中补全所有字段（`bilingual_enabled: false, bilingual_lang: 'zh', deepl_api_key: ''`）。

---

## 4. 与设计文档的一致性

### 已满足
1. 多语言模型已切换到 `ggml-medium.bin`。
2. 抗幻觉参数已按设计文档配置（`-et 2.4 -lpt -1.0 -bo 5 -bs 5`）。
3. SRT 后处理检测重复率 > 70% 判定幻觉，逻辑正确。
4. 双语字幕流程：DeepL 直接翻译原文 SRT → 合并双语，简洁高效。
5. 标签自动配色和软删除恢复已实现。
6. 设置页 UI 条件展示（开启双语后才显示语言和 Key 配置）。

### 仍有偏差/风险
1. **API Key 安全**：设计文档未提及安全存储方案，明文存储存在泄露隐患（见 S1）。
2. ~~**HTTP 超时**：设计文档提到"设置超时重试"，但实际 DeepL 调用无超时无重试（见 S1）。~~ ✅ 已修复
3. ~~**语言检测准确性**：依赖 whisper 输出格式解析，无 fallback（见 S2）。~~ ✅ 已修复

---

## 5. 测试覆盖评估

### 现有覆盖
- 编译通过 `wails build`（前后端均编译）。
- 通过 `wails dev` + 浏览器验证了标签加载和 UNIQUE 约束错误。

### 主要缺口
1. **字幕服务无单元测试**：`validateSRT`、`parseSRTEntries`、`mergeBilingualSRT`、`isSameLanguage` 均为纯函数，非常适合但缺少单元测试。
2. **DeepL 集成无 Mock 测试**：`translateDeepL` 依赖外部 API，但未抽象 HTTP 客户端接口进行 Mock。
3. **标签软删除恢复无回归测试**：创建→删除→重建流程未在 `video_service_test.go` 中覆盖。
4. **设置保存完整性**：`UpdateSettings` 逐字段复制，新增字段时容易遗漏，缺少全字段对比测试。

---

## 6. 架构级优化建议

1. **敏感配置分离**：将 API Key 等敏感信息从 SQLite 迁移到系统密钥链（`github.com/zalando/go-keyring`），数据库仅存非敏感配置。
2. **HTTP 客户端抽象**：为 DeepL 等外部服务创建 `Translator` 接口（`Translate(texts []string, targetLang string) ([]string, error)`），便于 Mock 测试和未来替换其他翻译服务（如 Google/百度）。
3. **字幕服务拆分**：当前 `subtitle_service.go` 已达 732 行，建议将 DeepL 翻译逻辑独立为 `translation_service.go`，SRT 解析工具函数独立为 `srt_utils.go`。
4. **Settings 更新改为全量覆盖**：当前 `UpdateSettings` 逐字段复制易遗漏，可改为 `database.DB.Model(&settings).Updates(input)` 直接更新非零值字段，或使用 `Omit("ID", "UpdatedAt")` 全量覆盖。

---

## 7. 审查结论

本轮新增功能覆盖面广（字幕多语言 + 双语翻译 + 标签优化），整体实现逻辑正确、流程清晰。主要风险集中在：

1. **安全层面**：API Key 明文存储是最需要优先处理的问题（S1）。
2. ~~**稳定性层面**：DeepL HTTP 无超时控制可能导致流程卡死（S1）。~~ ✅ 已修复
3. **可维护性**：`goto` 控制流、732 行大文件需要后续重构。
4. **测试短板**：新增纯函数多但测试覆盖为零，建议优先补充 `validateSRT` 和 `parseSRTEntries` 的单元测试。
