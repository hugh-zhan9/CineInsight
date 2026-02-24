# 字幕生成功能技术设计文档

**版本**: 2.0
**状态**: 已实现

## 1. 架构概览

本功能采用 **Sidecar (伴生程序)** 模式，应用主进程负责管理外部工具 (`ffmpeg`) 和模型文件 (`whisper model`) 的生命周期。

### 核心组件
1.  **DependencyManager**: 负责检测、下载和校验外部依赖。
2.  **SubtitleService**: 负责编排音频提取、字幕生成和双语翻译流程。
3.  **Whisper CLI**: 通过命令行调用 `whisper-cpp` 进行语音识别。
4.  **DeepL API**: 可选的翻译服务，用于生成双语字幕。

## 2. 详细设计

### 2.1 依赖管理 (DependencyManager)

#### 目录结构
所有资源存储在 `App.UserDataDir` 下：
```
~/.video-master/
├── bin/
│   └── ffmpeg          # 可执行文件 (Windows下为 ffmpeg.exe)
└── models/
    └── ggml-medium.bin  # Whisper 多语言模型 (~1.5GB)
```

#### 下载源 (Hardcoded for MVC)
为简化实现，初期硬编码常用稳定源：
- **FFmpeg (macOS)**: 通过 Homebrew 安装 (`brew install ffmpeg`)
- **Whisper (macOS)**: 通过 Homebrew 安装 (`brew install whisper-cpp`)
- **Model**: `https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin` (约 1.5GB，多语言)

### 2.2 后端接口 (Go)

#### `SubtitleService` 结构
```go
type SubtitleService struct {
    binDir   string
    modelDir string
    ctx      context.Context
}
```

#### Wails API 方法
前端可调用的方法（挂载在 `App` 上）：

1.  **CheckDependencies() (map[string]bool, error)**
    *   返回 `{ "ffmpeg": true, "model": false }` 表示 ffmpeg 存在但模型缺失。

2.  **DownloadDependencies() error**
    *   触发后台下载任务。
    *   通过事件 `download-progress` 推送进度。

3.  **GenerateSubtitle(videoID uint) error**
    *   入口方法，执行全流程。
    *   前置检查依赖，若缺失返回特定错误。
    *   通过事件 `subtitle-progress` 推送进度。
    *   成功后生成 SRT 文件到视频同级目录。

### 2.3 业务流程

#### A. 依赖下载流程
1.  前端调用 `DownloadDependencies`。
2.  后端创建 `bin` 和 `models` 目录。
3.  **并行/串行下载**：
    *   下载 FFmpeg Zip -> 解压 -> 移动到 `bin/` -> `chmod +x`。
    *   下载 Model Bin -> 保存到 `models/`。
4.  下载过程中每写入 chunk 发送一次 `download-progress` 事件。

#### B. 字幕生成流程
1.  前端调用 `GenerateSubtitle(id)`。
2.  后端查询 DB 获取 `Video` 信息。
3.  **音频提取 (FFmpeg)**:
    *   命令: `ffmpeg -i input.mp4 -ar 16000 -ac 1 -c:a pcm_s16le output.wav -y`
    *   输出临时文件 `temp/video_id.wav`。
4.  **模型推理 (Whisper)**:
    *   调用 `whisper-cli` 处理 wav 数据。
    *   使用多语言 medium 模型 (`ggml-medium.bin`)。
    *   包含抗幻觉参数：`-l auto --no-fallback -et 2.4 -lpt -1.0 -bo 5 -bs 5`。
    *   自动检测音频语言。
5.  **后处理校验**:
    *   检测 SRT 中文本重复率，超过 70% 判定为模型幻觉，报错删除文件。
6.  **双语翻译 (可选)**:
    *   若用户开启双语字幕且检测语言 ≠ 目标语言：
    *   调用 DeepL API 翻译原文 SRT（支持任意语言对，自动检测源语言）。
    *   合并为双语 SRT（每条字幕：上行原文，下行翻译）。
7.  **结果转换**:
    *   写入 `video_path.srt` (与视频同名)。
8.  清理临时 WAV 文件。
9.  推送 `subtitle-success`。

### 2.4 Settings 字段

| 字段 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `bilingual_enabled` | bool | false | 是否开启双语字幕 |
| `bilingual_lang` | string | "zh" | 目标翻译语言代码 |
| `deepl_api_key` | string | "" | DeepL API Key |

### 2.4 事件定义 (Events)

| 事件名 | 数据结构 (JSON) | 说明 |
| :--- | :--- | :--- |
| `download-progress` | `{ "component": "ffmpeg", "percent": 45, "msg": "下载中..." }` | 组件下载进度 |
| `subtitle-start` | `{ "videoID": 123 }` | 开始任务 |
| `subtitle-progress` | `{ "videoID": 123, "stage": "extract/transcribe", "percent": 10 }` | 处理进度 |
| `subtitle-success` | `{ "videoID": 123, "path": "/path/to/video.srt" }` | 成功 |
| `subtitle-error` | `{ "videoID": 123, "error": "file not found" }` | 失败 |

### 2.5 异常处理
- **网络超时**: 下载和 DeepL API 调用设置超时重试。
- **权限问题**: macOS 首次运行 ffmpeg 可能触发安全警告。
- **模型幻觉**: 后处理检测重复输出，超阈值自动报错。
- **DeepL 错误**: API Key 无效(403)、额度用完(456) 等均有友好提示。
- **翻译失败降级**: 双语翻译失败时保留原文 SRT，不影响基本功能。

## 3. 前端交互设计

### 状态机 (Store)
需要在前端维护一个 `SubtitleStore`：
- `dependenciesReady`: bool
- `downloading`: bool
- `processingVideoIds`: Set<uint> (支持多个视频排队或单任务)

### UI 组件
- **VideoListPage**:
    - "CC" 按钮：
        - 灰色: 依赖未就绪 (点击 -> 弹窗下载)
        - 绿色/常态: 依赖就绪 (点击 -> 生成)
        - 旋转/加载中: 生成中
        - 蓝色: 已有字幕 (点击 -> 打开目录/重新生成)
