# 析微影策

A cross-platform desktop video manager featuring smart random playback, tag management, preview-first browsing, and offline bilingual subtitle generation.
(一款跨平台本地视频管理应用，支持智能随机播放、多维度标签库、预览优先浏览及离线双语字幕生成。)

## 功能特性

- **视频扫描**: 支持自定义视频格式，支持启动后台增量扫描自动追平目录变更。
- **文件迁移检测**: 扫描时自动识别文件移动（name+size 指纹匹配），保留标签等元数据。
- **智能播放**: 内置加权随机播放算法，支持在当前搜索、标签、体积、分辨率和智能视图内按均衡、未看或收藏随机，并避免短期重复。
- **播放可靠性修复**: 正式播放失败时会明确提示具体文件，失败不污染播放统计，并标记失效记录用于后续纠偏。
- **可恢复回收站**: 删除记录或原文件后可在应用内恢复；恢复时绝不覆盖原路径上的现有文件。
- **预览与续播**: 支持右侧抽屉内嵌预览、观看进度和断点续播；字幕命中跳转优先，无法内嵌时可退化为统计中立的系统播放器预览。
- **单页媒体详情**: 右侧抽屉连续展示预览、显示/原始标题、0–10 半分制个人评分、演员、作品集和只读技术信息，不使用标签页，也不会因编辑标题而重命名文件。
- **人物与作品集**: 支持同名独立人物、托管头像，以及可多重归属、可拖拽排序的作品集和托管封面；人物和作品集均有独立片库视图。
- **本地技术快照**: 使用本地 `ffprobe` 保存容器、视频流、音轨、内封/外置字幕信息；失败保留最后成功快照，旧片库通过显式、可取消、可续跑的单 worker 任务补全。
- **可视化片库**: 列表和响应式网格自由切换，缩略图按需生成并在源文件变化后自动刷新。
- **智能与保存视图**: 内置继续观看、收藏、最近播放、未看、已看、最近添加、未打标签、无字幕和路径失效视图；个人评分可筛选和排序，并随命名视图保存。
- **AI 字幕生成**: 基于 WhisperX 运行时与 DeepL 翻译，离线生成高精度双语字幕，支持取消和强制生成。
- **多维检索**: 支持输入即搜的名称过滤与多重标签组合过滤。
- **字幕命中预览**: 字幕搜索结果显示命中时间，点击后可直接跳到对应画面。
- **标签管理**: 支持 12 色智能自动分配、透明度显示、输入即搜过滤、软删除恢复。
- **视频重命名**: 支持同时重命名磁盘文件和数据库记录，自动保留扩展名。
- **轻量可靠**: 使用 Postgres 持久化存储，支持游标分页与失效记录纠偏。
- **统一清理审阅**: 在同一入口审阅精确重复、短/低清和 AI 已检测同源候选；同源项不自动选择，删除统一进入可恢复回收站。
- **现代化 UI**: 基于 Vue 3 的视频工作台，主列表支持持续加载和虚拟化，网格适合视觉浏览。
- **右键菜单**: 快速播放、定位文件、重命名或安全删除记录。

## 技术栈

- **后端**: Go + GORM + Postgres
- **前端**: Vue 3 + Vite
- **框架**: Wails v2
- **数据库**: Postgres

## 开发环境要求

- Go 1.23+
- Node.js 20.19+（20.x）、22.13+（22.x）或 24+
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

### macOS 一键打包并替换旧应用

```bash
# 构建并替换 /Applications/析微影策.app
bash scripts/build_and_install_app.sh

# 仅替换已构建好的产物
bash scripts/build_and_install_app.sh --skip-build

# 透传额外的 wails build 参数
bash scripts/build_and_install_app.sh -clean
```

脚本会在安装前关闭正在运行的应用，并在必要时通过 `sudo` 写入 `/Applications`。

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
6. **搜索和组织**: 使用顶部搜索框、标签与智能视图筛选；常用组合可保存为命名视图
7. **浏览与维护详情**: 点击“预览”打开右侧单页详情，维护标题、评分、人物和作品集，并查看只读媒体流信息
8. **浏览实体片库**: 使用顶部“人物 / 作品集”入口查看关联作品、头像、封面和作品集顺序
9. **补全旧片库**: 点击“补全技术信息”显式启动后台任务；任务可取消，再次启动会跳过已完成项
10. **按条件随机**: 选择均衡、未看或收藏模式，在当前筛选范围内随机播放
11. **播放/打开**: 点击“播放”使用默认播放器，点击“打开目录”查看文件位置
12. **安全清理**: 在清理候选中预览重复与同源版本，确认后移入回收站

## 数据存储

结构化数据存储在 Postgres 数据库中，连接信息通过 `.env` 提供。头像和作品集封面复制到 `~/.video-master/media-details/`，不依赖原始图片路径。

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
│   ├── media_probe_service.go
│   ├── technical_backfill_service.go
│   ├── video_detail_service.go
│   ├── person_service.go
│   ├── collection_service.go
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
            ├── EntityLibraryPage.vue
            ├── VideoListPage.vue
            ├── VideoListRow.vue
            └── VirtualVideoList.vue
```

## 许可证

MIT License
