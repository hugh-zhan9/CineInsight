# 需求：离线视频字幕生成 (Whisper.cpp)

**创建时间**: 2026-02-14
**状态**: 规划中

## 1. 需求背景
用户希望在本地视频管理应用中，能够为选定的视频自动生成字幕文件（.srt/.vtt），以便于搜索内容或辅助观看。考虑到隐私和便捷性，功能需完全离线运行，且不应强制用户手动安装复杂的依赖环境。

## 2. 核心功能
*   **一键生成**: 在视频列表项中提供 "CC" 按钮，一键启动生成流程。
*   **离线识别**: 使用 `whisper.cpp` (OpenAI Whisper 模型的 C++ 移植) 在本地 CPU/GPU 上运行。
*   **自动依赖管理 (Sidecar)**:
    *   应用自动检测并从网络下载轻量级 `ffmpeg` (用于提取音频)。
    *   应用自动下载 Whisper 模型文件 (`ggml-base.en.bin` 等)。
    *   用户无需手动配置环境。
*   **进度反馈**: 实时展示“下载依赖”、“提取音频”、“AI 识别中”等进度状态。

## 3. 技术方案 (Go + Vue + Wails)

### 3.1 目录结构
应用将使用用户主目录下的 `.video-master` 作为数据目录：
- `~/.video-master/bin/ffmpeg` (及 `ffprobe`)
- `~/.video-master/models/ggml-base.en.bin`

### 3.2 后端设计 (Go)
新增 `SubtitleService` 服务：
1.  **CheckDependencies()**: 检查上述文件是否存在。
2.  **DownloadDependencies()**: 从 GitHub Releases 或指定 CDN 下载缺失的二进制文件和模型。
3.  **GenerateSubtitle(videoID)**:
    *   利用 `ffmpeg` 提取音频流并转换为 16kHz WAV (Whisper 要求)。
    *   调用 `whisper.cpp` 绑定库处理 WAV 文件。
    *   将识别结果输出为 `.srt` 文件到视频同级目录。
    *   通过 Wails Events 推送实时进度。

### 3.3 前端设计 (Vue)
*   **VideoListPage.vue**:
    *   增加字幕图标按钮。
    *   未就绪状态：点击弹出“下载组件”确认框 -> 显示下载进度条。
    *   就绪状态：点击进入“生成中”状态（转圈/百分比）。
    *   完成状态：提示成功，按钮高亮。

## 4. 交付计划
1.  **Phase 1**: 依赖管理模块 (下载/校验)。
2.  **Phase 2**: 音频提取与 Whisper 集成。
3.  **Phase 3**: 前端 UI 与交互串联。
