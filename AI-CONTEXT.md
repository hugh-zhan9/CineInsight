# AI-CONTEXT.md: 项目核心上下文 (Single Source of Truth)

> 本文件是本项目所有 AI 助手（Gemini, Claude, GPT 等）的权威上下文来源。

## 1. 项目架构与技术栈 (Architecture & Stack)

本项目是一个基于 **Wails v2** 的跨平台桌面视频管理工具，结合了 Go 后端的高性能和 Vue 3 前端的高效开发体验。

- **后端 (Go 1.23+):**
  - **框架:** Wails v2 (负责桥接 Go 方法到前端、窗口管理、事件分发)。
  - **数据库:** SQLite 3 (通过 **GORM** 驱动)，实现数据本地持久化。
  - **业务逻辑:** 封装在 `services/` 目录下（如 `VideoService`, `SubtitleService`, `TagService`, `DirectoryService`）。
- **前端 (Vue 3 + Vite):**
  - **UI 框架:** 原生 CSS + Vue 3 组合式 API (Composition API)。
  - **通信:** 通过 `wailsjs/go` 自动生成的绑定调用后端方法，使用 `wailsjs/runtime` 进行事件监听。
- **外部依赖 (Sidecars):**
  - **FFmpeg:** 用于提取视频音频流（16kHz, mono, WAV）。
  - **Whisper.cpp:** 用于本地离线语音识别生成字幕。

## 2. 核心功能与实现原理 (Core Features)

### 2.1 智能随机播放 (Smart Random Play)
采用自研加权随机算法 (`ALGORITHM.md`)，旨在平衡视频库的播放频率：
- **公式:** `播放分数 = 普通播放次数 * PlayWeight + 随机播放次数`。
- **逻辑:** 分数越低的视频被选中的概率越高。
- **权重:** `PlayWeight` 可配置（默认 2.0）。

### 2.2 离线字幕生成 (Offline Subtitle Generation)
集成 AI 能力实现全本地化字幕制作：
- **流程:** 视频 -> FFmpeg (提取 16kHz WAV) -> Whisper.cpp (推理识别) -> .srt 文件。
- **依赖管理:** `SubtitleService` 负责自动检测系统路径及 Homebrew 路径下的依赖。

### 2.3 稳定分页机制 (Cursor-based Pagination)
针对大规模视频列表设计了基于游标的稳定分页：
- **排序规则:** `score ASC, size DESC, id DESC`。

### 2.4 视频扫描与路径管理
- **扫描机制:** 递归遍历目录，基于 `Settings` 中的 `VideoExtensions` 过滤。
- **唯一性:** 在数据库层面通过 `idx_videos_path_active` 唯一索引（结合 `deleted_at IS NULL`）保证路径唯一。

## 3. 关键目录说明 (Directory Structure)

- `/services`: **核心业务层**（Video, Subtitle, Tag, Directory 服务）。
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
