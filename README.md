# 析微影策

A cross-platform desktop video manager featuring smart random playback, tag management, preview-first browsing, and offline bilingual subtitle generation.
(一款跨平台本地视频管理应用，支持智能随机播放、多维度标签库、预览优先浏览及离线双语字幕生成。)

## 功能特性

- **视频扫描**: 支持自定义视频格式，支持启动后台增量扫描自动追平目录变更。
- **文件迁移检测**: 扫描时自动识别文件移动（name+size 指纹匹配），保留标签等元数据。
- **智能播放**: 内置加权随机播放算法，平衡内容分发，支持配置基础权重。
- **播放可靠性修复**: 正式播放失败时会明确提示具体文件，失败不污染播放统计，并标记失效记录用于后续纠偏。
- **预览抽屉**: 支持右侧抽屉内嵌预览；无法内嵌时可退化为统计中立的系统播放器预览。
- **AI 字幕生成**: 基于 WhisperX 运行时与 DeepL 翻译，离线生成高精度双语字幕，支持取消和强制生成。
- **多维检索**: 支持输入即搜的名称过滤与多重标签组合过滤。
- **标签管理**: 支持 12 色智能自动分配、透明度显示、输入即搜过滤、软删除恢复。
- **视频重命名**: 支持同时重命名磁盘文件和数据库记录，自动保留扩展名。
- **轻量可靠**: 使用 Postgres 持久化存储，支持游标分页与失效记录纠偏。
- **现代化 UI**: 基于 Vue 3 的单列视频工作台，主列表支持持续加载；首页主列表已引入虚拟列表以降低长列表卡顿。
- **右键菜单**: 快速播放、定位文件、重命名或安全删除记录。

## 技术栈

- **后端**: Go + GORM + Postgres
- **前端**: Vue 3 + Vite
- **框架**: Wails v2
- **数据库**: Postgres

## 开发环境要求

- Go 1.23+
- Node.js 16+
- Wails CLI v2
- Postgres 12+

## 安装依赖

```bash
# 安装 Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 进入项目目录
cd /Users/zhangyukun/project/CineInsight

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
构建后的应用位于 `build/bin/析微影策.app`

### Windows
构建后的应用位于 `build/bin/析微影策.exe`

### Linux
构建后的应用位于 `build/bin/析微影策`

## 使用说明

1. **首次使用**: 启动应用后点击"扫描目录"按钮
2. **选择目录**: 选择包含视频文件的文件夹
3. **开始扫描**: 点击"开始扫描"，应用会自动导入所有视频
4. **管理标签**: 点击"管理标签"创建自定义标签
5. **添加标签**: 在视频列表中点击"+ 标签"为视频添加标签
6. **搜索视频**: 使用顶部搜索框按名称搜索，或点击标签筛选
7. **预览视频**: 点击"预览"在右侧抽屉中查看视频；无法内嵌预览时可切换到系统播放器预览
8. **播放/打开**: 点击"播放"使用默认播放器，点击"打开目录"查看文件位置
9. **删除视频**: 点击"删除"可选择是否同时删除原始文件

## 数据存储

应用数据存储在 Postgres 数据库中，连接信息通过 `.env` 提供。

示例 `.env`：

```bash
PG_HOST=127.0.0.1
PG_PORT=5432
PG_USER=video
PG_PASSWORD=your_password
PG_DB=video_master
PG_SSLMODE=disable
PG_TIMEZONE=Asia/Shanghai
```

如果需要从旧版 SQLite 迁移数据，可运行迁移脚本：

```bash
go run ./cmd/migrate_sqlite_to_pg
# 或指定 sqlite 路径
go run ./cmd/migrate_sqlite_to_pg --sqlite ~/.video-master/video-master.db
```

## 项目结构

```
video-master/
├── app.go                 # Wails 应用入口
├── main.go               # 主程序
├── preview_asset_handler.go # 预览媒体资源处理
├── models/               # 数据模型
│   └── video.go
├── database/             # 数据库层
│   └── database.go
├── services/             # 业务逻辑层
│   ├── playback_result.go
│   ├── preview_service.go
│   ├── video_service.go
│   ├── subtitle_service.go
│   ├── tag_service.go
│   ├── directory_service.go
│   └── settings_service.go
└── frontend/             # Vue 前端
    └── src/
        ├── App.vue
        └── components/
            ├── PreviewDrawer.vue
            ├── VideoListPage.vue
            ├── VideoListRow.vue
            └── VirtualVideoList.vue
```

## 许可证

MIT License
