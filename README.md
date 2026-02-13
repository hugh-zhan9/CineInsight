# 视频管理器

一个用 Golang 和 Wails 开发的现代化桌面视频管理应用。

## 功能特性

- **视频扫描**: 扫描指定目录下的所有视频文件（支持 mp4, avi, mkv, mov, wmv, flv, webm, m4v）
- **搜索功能**: 支持按文件名关键词模糊搜索
- **标签管理**: 为视频添加彩色标签，支持按标签筛选
- **右键菜单**: 支持右键快捷操作
  - 打开文件所在目录
  - 使用系统默认播放器播放
  - 删除视频
- **删除确认**: 可选择是否删除原始文件，支持"不再提醒"选项
- **轻量数据库**: 使用内嵌 SQLite 数据库，开箱即用
- **跨平台**: 支持 macOS, Windows, Linux

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
│   ├── tag_service.go
│   └── settings_service.go
└── frontend/             # Vue 前端
    └── src/
        └── App.vue       # 主界面
```

## 许可证

MIT License
