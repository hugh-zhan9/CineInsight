# Video Master (视频管理器)

A cross-platform desktop video manager featuring smart random playback, tag management, and offline bilingual subtitle generation.
(一款跨平台本地视频管理应用，支持智能随机播放、多维度标签库及离线双语字幕生成。)

## 功能特性

- **视频扫描**: 支持自定义视频格式，支持启动后台增量扫描自动追平目录变更。
- **智能播放**: 内置加权随机播放算法，平衡内容分发，支持配置基础权重。
- **AI 字幕生成**: 内置 Whisper 模型与 DeepL 翻译，离线生成高精度原文+翻译双语字幕。
- **多维检索**: 支持输入即搜的名称过滤与多重标签组合过滤。
- **标签管理**: 支持软删除恢复与 12 色智能自动分配颜色。
- **轻量可靠**: 内置 SQLite 单文件数据库，支持海量视频百万级游标秒查。
- **现代化 UI**: 基于 Vue 3 的响应式瀑布流列表，支持无限下拉加载。
- **右键菜单**: 快速调用系统默认播放器、定位文件或安全删除记录。

## 技术栈

- **后端**: Go + GORM + SQLite
- **前端**: Vue 3 + Vite
- **框架**: Wails v2
- **数据库**: SQLite（内嵌，无需额外安装）

## 开发环境要求

- Go 1.18+
- Node.js 16+
- Wails CLI v2

## 安装依赖

```bash
# 安装 Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 进入项目目录
cd video-master

# 安装 Go 依赖
go mod download

# 安装前端依赖
cd frontend && npm install && cd ..
```

## 开发模式运行

```bash
export PATH=$PATH:$HOME/go/bin
wails dev
```

## 构建生产版本

```bash
# 构建桌面应用
export PATH=$PATH:$HOME/go/bin
wails build

# 构建产物位于: build/bin/
```

### macOS
构建后的应用位于 `build/bin/video-master.app`

### Windows
构建后的应用位于 `build/bin/video-master.exe`

### Linux
构建后的应用位于 `build/bin/video-master`

## 使用说明

1. **首次使用**: 启动应用后点击"扫描目录"按钮
2. **选择目录**: 选择包含视频文件的文件夹
3. **开始扫描**: 点击"开始扫描"，应用会自动导入所有视频
4. **管理标签**: 点击"管理标签"创建自定义标签
5. **添加标签**: 在视频列表中点击"+ 标签"为视频添加标签
6. **搜索视频**: 使用顶部搜索框按名称搜索，或点击标签筛选
7. **播放/打开**: 点击"播放"使用默认播放器，点击"打开目录"查看文件位置
8. **删除视频**: 点击"删除"可选择是否同时删除原始文件

## 数据存储

应用数据存储在用户目录下的 `.video-master` 文件夹：
- macOS/Linux: `~/.video-master/video-master.db`
- Windows: `%USERPROFILE%\.video-master\video-master.db`

## 项目结构

```
video-master/
├── app.go                 # Wails 应用入口
├── main.go               # 主程序
├── models/               # 数据模型
│   └── video.go
├── database/             # 数据库层
│   └── database.go
├── services/             # 业务逻辑层
│   ├── video_service.go
│   ├── subtitle_service.go
│   ├── tag_service.go
│   ├── directory_service.go
│   └── settings_service.go
└── frontend/             # Vue 前端
    └── src/
        └── App.vue       # 主界面
```

## 许可证

MIT License
