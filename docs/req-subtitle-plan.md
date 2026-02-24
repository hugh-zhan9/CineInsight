# 需求：离线视频字幕生成 (Whisper.cpp)

**创建时间**: 2026-02-14
**最后更新**: 2026-02-24
**状态**: 已实现

## 1. 需求背景
用户希望在本地视频管理应用中，能够为选定的视频自动生成字幕文件（.srt/.vtt），以便于搜索内容或辅助观看。考虑到隐私和便捷性，功能需完全离线运行，且不应强制用户手动安装复杂的依赖环境。

## 2. 核心功能
*   **一键生成**: 在视频列表项中提供字幕按钮，一键启动生成流程。
*   **离线识别**: 使用 `whisper.cpp` 在本地 CPU/GPU 上运行。
*   **多语言支持**: 使用 `ggml-medium.bin` 多语言模型，自动检测视频语言。
*   **抗幻觉处理**: 包含 entropy/logprob 阈值、beam search 等参数。后处理检测重复输出。
*   **双语字幕 (可选)**: 集成 DeepL API，翻译为用户指定的目标语言，合并为双语 SRT。
*   **自动依赖管理**: 通过 Homebrew 安装 ffmpeg/whisper-cpp，自动下载模型文件。
*   **进度反馈**: 实时展示下载、提取、识别、翻译等进度状态。

## 3. 技术方案 (Go + Vue + Wails)

### 3.1 目录结构
应用将使用用户主目录下的 `.video-master` 作为数据目录：
- `~/.video-master/models/ggml-medium.bin` (多语言模型，约 1.5GB)

### 3.2 后端设计 (Go)
`SubtitleService` 服务：
1.  **CheckDependencies()**: 检查 ffmpeg、whisper-cli、模型文件。
2.  **DownloadDependencies()**: Homebrew 安装 + 模型下载。
3.  **GenerateSubtitle(videoID)**:
    *   `ffmpeg` 提取音频 -> 16kHz WAV。
    *   `whisper-cli` 转录（含抗幻觉参数 + 自动语言检测）。
    *   后处理检测幻觉（重复率 > 70%）。
    *   可选：DeepL API 翻译 -> 合并双语 SRT。
    *   输出 `.srt` 到视频同目录。

### 3.3 前端设计 (Vue)
*   **VideoListPage.vue**: 字幕按钮 + 进度弹窗。
*   **SettingsPage.vue**: 双语开关、目标语言选择、DeepL API Key 输入。

## 4. 交付计划
1.  **Phase 1**: 依赖管理模块。 ✅
2.  **Phase 2**: 多语言模型 + 抗幻觉参数。 ✅
3.  **Phase 3**: 前端 UI 串联。 ✅
4.  **Phase 4**: 双语字幕翻译（DeepL）。 ✅
