# AI-CONTEXT.md: 析微影策核心上下文 (Single Source of Truth)

> 本文件是“析微影策 (Xī Wēi Yǐng Cè)”项目所有 AI 助手（Gemini, Claude, GPT 等）的权威上下文来源。

## 1. 项目架构与技术栈 (Architecture & Stack)

本项目是一个基于 **Wails v2** 的跨平台桌面视频管理系统，命名为“析微影策”，旨在通过 AI 分析与智能策略提供极致的本地影视管理体验。

- **后端 (Go 1.23+):**
  - **框架:** Wails v2 (负责桥接 Go 方法到前端、窗口管理、事件分发)。
  - **数据库:** SQLite 3 (通过 **GORM** 驱动)，实现数据本地持久化。
  - **业务逻辑:** 封装在 `services/` 目录下（如 `VideoService`, `SubtitleService`, `TagService`, `DirectoryService`）。
- **前端 (Vue 3 + Vite):**
  - **UI 框架:** 原生 CSS + Vue 3 组合式 API (Composition API)。
  - **通信:** 通过 `wailsjs/go` 自动生成的绑定调用后端方法，使用 `wailsjs/runtime` 进行事件监听。
- **外部依赖 (Sidecars):**
  - **FFmpeg:** 用于提取视频音频流（16kHz, mono, WAV）。
  - **Whisper.cpp:** 用于本地离线语音识别生成字幕（多语言 medium 模型）。
  - **DeepL API (可选):** 用于双语字幕翻译（用户在设置页配置 API Key）。

## 2. 核心功能与实现原理 (Core Features)

### 2.1 智能随机播放 (Smart Random Play)
采用自研加权随机算法 (`ALGORITHM.md`)，旨在平衡视频库的播放频率：
- **公式:** `播放分数 = 普通播放次数 * PlayWeight + 随机播放次数`。
- **逻辑:** 分数越低的视频被选中的概率越高。
- **权重:** `PlayWeight` 可配置（默认 2.0）。

### 2.2 离线字幕生成 (Offline Subtitle Generation)
集成 AI 能力实现全本地化字幕制作：
- **模型:** `ggml-medium.bin`（多语言，~1.5GB），自动检测音频语言。
- **流程:** 视频 -> FFmpeg (提取 16kHz WAV) -> Whisper.cpp (推理识别) -> 后处理校验 -> .srt 文件。
- **抗幻觉:** `--no-fallback -et 2.4 -lpt -1.0 -bo 5 -bs 5`，后处理检测重复率 >85% 判定幻觉。
- **幻觉确认:** 检测到幻觉时弹窗询问用户，可选择强制生成保留结果 (`ForceGenerateSubtitle`)。
- **任务取消:** 字幕生成过程中可随时取消 (`CancelSubtitle`)，通过 `exec.CommandContext` 终止子进程。
- **双语字幕 (可选):** 开启后调用 DeepL API 翻译原文 -> 合并为双语 SRT（原文上行、翻译下行）。
- **依赖管理:** `SubtitleService` 负责自动检测系统路径及 Homebrew 路径下的依赖。

### 2.3 标签管理 (Tag Management)
- **自动配色:** 创建标签时自动从 12 色预设调色板中轮换分配颜色，用户无需手动选色。
- **透明度显示:** 标签背景色渲染时自动加 35% 透明度（hex→rgba），保证深色文字清晰可读。
- **搜索过滤:** 添加标签弹窗中输入框同时支持创建新标签和实时过滤已有标签。
- **软删除恢复:** 创建同名已删除标签时自动恢复（清除 `deleted_at`），避免唯一约束冲突。
- **改名防冲突:** 改名时检查活跃标签和软删除标签，自动清理废弃记录。

### 2.4 稳定分页机制 (Cursor-based Pagination)
针对大规模视频列表设计了基于游标的稳定分页：
- **排序规则:** `score ASC, size DESC, id DESC`。

### 2.5 视频扫描与路径管理
- **扫描机制:** 递归遍历目录，基于 `Settings` 中的 `VideoExtensions` 过滤。
- **附带大小:** `ScanDirectoryWithInfo` 返回 `[]ScannedFile`（含 path+size），用于迁移检测。
- **唯一性:** 在数据库层面通过 `idx_videos_path_active` 唯一索引（结合 `deleted_at IS NULL`）保证路径唯一。

### 2.6 文件迁移检测
- **应用场景:** 自动扫描时区分“文件移走”和“文件删除”，移走的文件更新路径而非删除重建。
- **匹配算法:** 用 name + size 指纹对 stale 记录和新文件配对，配对成功调用 `RelocateVideo` 保留标签等元数据。
- **匹配范围:** 全库匹配，不限于当前目录。

### 2.7 视频重命名
- **功能:** `RenameVideo` 同时重命名磁盘文件和数据库记录（name/path）。
- **安全:** 自动保留原扩展名，目标文件已存在时拒绝操作，数据库更新失败时回滚文件名。

## 3. 关键目录说明 (Directory Structure)

- `/services`: **核心业务层**（Video, Subtitle, Tag, Settings, Directory 服务）。
- `/models`: **数据模型层**（GORM 结构体定义）。
- `/database`: **持久化层**（SQLite 连接与迁移）。
- `/frontend/src/components`: **UI 组件**（Vue 组件）。

## 4. 开发与构建指南 (Development & Build)

- **开发模式:** `wails dev`
- **构建应用:** `wails build`
- **数据库路径:** `~/.video-master/video-master.db`

## 5. 开发规范与后续演进

- **规范:** Go 方法导出 PascalCase，JSON 映射 snake_case。
- **代办:** 集成 `ffprobe` 获取时长，完善 Windows 下的 Whisper 自动下载。
