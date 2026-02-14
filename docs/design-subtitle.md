# 字幕生成功能技术设计文档

**版本**: 1.0
**状态**: 拟定中

## 1. 架构概览

本功能采用 **Sidecar (伴生程序)** 模式，应用主进程负责管理外部工具 (`ffmpeg`) 和模型文件 (`whisper model`) 的生命周期。

### 核心组件
1.  **DependencyManager**: 负责检测、下载和校验外部依赖。
2.  **SubtitleService**: 负责编排音频提取和字幕生成流程。
3.  **Whisper Binding**: 通过 CGO 调用 `whisper.cpp` 进行推理。

## 2. 详细设计

### 2.1 依赖管理 (DependencyManager)

#### 目录结构
所有资源存储在 `App.UserDataDir` 下：
```
~/.video-master/
├── bin/
│   └── ffmpeg          # 可执行文件 (Windows下为 ffmpeg.exe)
└── models/
    └── ggml-base.en.bin # Whisper模型文件
```

#### 下载源 (Hardcoded for MVC)
为简化实现，初期硬编码常用稳定源：
- **FFmpeg (macOS)**: `https://evermeet.cx/ffmpeg/getrelease/zip` -> 解压取 `ffmpeg`
- **Model**: `https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin` (约 148MB)

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
    *   加载 `models/ggml-base.en.bin`。
    *   调用 `whisper.Context.Process` 处理 wav 数据。
5.  **结果转换**:
    *   遍历 Segment，格式化为 SRT 时间轴格式。
    *   写入 `video_path.srt` (与视频同名)。
6.  清理临时 WAV 文件。
7.  推送 `subtitle-success`。

### 2.4 事件定义 (Events)

| 事件名 | 数据结构 (JSON) | 说明 |
| :--- | :--- | :--- |
| `download-progress` | `{ "component": "ffmpeg", "percent": 45, "msg": "下载中..." }` | 组件下载进度 |
| `subtitle-start` | `{ "videoID": 123 }` | 开始任务 |
| `subtitle-progress` | `{ "videoID": 123, "stage": "extract/transcribe", "percent": 10 }` | 处理进度 |
| `subtitle-success` | `{ "videoID": 123, "path": "/path/to/video.srt" }` | 成功 |
| `subtitle-error` | `{ "videoID": 123, "error": "file not found" }` | 失败 |

### 2.5 异常处理
- **网络超时**: 下载设置超时重试。
- **权限问题**: macOS 首次运行 ffmpeg 可能触发安全警告（需处理或提示用户在设置中允许）。
- **CGO 崩溃**: Whisper CGO 调用需做好 Panic 捕获 (Recover)。

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
