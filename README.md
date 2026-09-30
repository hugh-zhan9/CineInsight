# 析微影策

A cross-platform desktop video manager featuring smart random playback, tag management, preview-first browsing, and offline bilingual subtitle generation.
(一款跨平台本地视频管理应用，支持智能随机播放、多维度标签库、预览优先浏览及离线双语字幕生成。)

## 功能特性

- **视频扫描**: 支持自定义视频格式，支持启动后台增量扫描自动追平目录变更。
- **文件迁移检测**: 扫描时自动识别文件移动（name+size 指纹匹配），保留标签等元数据。
- **智能播放**: 内置带时间半衰期的加权随机播放算法，支持在当前搜索、标签、人物、体积、分辨率和智能视图内按均衡、仅未看或仅收藏随机，并避免短期重复（最近 12 次，重启后仍记得）；30 秒内可「换一个」，换掉的那次不计入统计。
- **播放可靠性修复**: 正式播放失败时会明确提示具体文件，失败不污染播放统计，并标记失效记录用于后续纠偏。
- **可恢复回收站**: 删除原文件时移到 macOS 系统废纸篓（所在磁盘不支持废纸篓时由你选择永久删除或只从片库移除）；只删除记录时按文件身份屏蔽、可「允许重新收录」。视频与图片都能在回收站中心恢复、批量撤销，恢复时绝不覆盖原路径上的现有文件。
- **预览与续播**: 支持右侧抽屉内嵌预览、进度条 sprite 悬停画面、观看进度和断点续播；字幕命中跳转优先，无法内嵌时可退化为统计中立的系统播放器预览。
- **单页媒体详情**: 右侧抽屉连续展示预览、显示/原始标题、0–10 半分制个人评分、演员、作品集和只读技术信息，不使用标签页，也不会因编辑标题而重命名文件。
- **人物与作品集**: 支持同名独立人物、托管头像，以及可多重归属、可拖拽排序的作品集和托管封面；人物和作品集均有独立片库视图。
- **本地技术快照**: 使用本地 `ffprobe` 保存容器、视频流、音轨、内封/外置字幕信息；失败保留最后成功快照，旧片库通过显式、可取消、可续跑的单 worker 任务补全。
- **可视化片库**: 列表和响应式网格自由切换，缩略图按需生成并在源文件变化后自动刷新。
- **智能与保存视图**: 内置继续观看、收藏、点赞、最近播放、未看、已看、最近添加、未打标签、无字幕、本地资料有更新和路径失效视图（路径失效按原因分组，可重新定位或重新检查）；个人评分可筛选和排序，并随命名视图保存，保存视图可更新与重命名。
- **任务中心与待处理**: 顶栏「任务」汇总全部后台任务的进度、等待原因与失败明细，「待处理」汇总各类待审阅事项并一键跳到对应面板；有任务在跑时退出应用会先确认。
- **AI 字幕生成**: 基于 WhisperX 运行时与 DeepL / OpenAI 兼容接口翻译，离线生成高精度双语字幕，支持取消和强制生成；覆盖已有字幕前自动备份（每个视频保留最近 5 份，可恢复），任务重启后可继续处理。
- **多维检索**: 支持文件、字幕与可选 pgvector 语义搜索；语义结果可继续组合标签、体积、分辨率和智能视图筛选，并可从详情抽屉“找相似”。
- **字幕命中预览**: 字幕搜索结果显示命中时间，点击后可直接跳到对应画面。
- **标签管理**: 支持 12 色智能自动分配、透明度显示、输入即搜过滤、软删除恢复。
- **视频重命名**: 支持同时重命名磁盘文件和数据库记录，自动保留扩展名。
- **轻量可靠**: 默认使用本机 SQLite 数据库，无需额外安装；也可切换到 PostgreSQL。支持游标分页与失效记录纠偏。
- **统一清理审阅**: 在同一入口（⌘K）审阅精确重复、感知哈希近似重复、截取片段、极短/极低清和 AI 已检测同源候选；保留建议优先整理过的那一份，默认只预选精确重复，删除前可把标签、人物、收藏等元数据合并到保留项，删除统一进入可恢复回收站。
- **数据安全与可迁移元数据**: 设置页内置数据库自动备份与一键恢复（SQLite 与 PostgreSQL 都支持），可在两个后端之间迁移切换；另有单视频/当前筛选批量 Kodi NFO 写出。
- **片库洞察**: 独立洞察页展示总量、时长、观看覆盖率、存储分布、按来源分列的观看记录热力图、评分和 AI 标签分布。
- **审阅效率**: 支持 J/K、方向键、空格、F、W、T、回车等固定快捷键；设置页可随时查看说明。
- **浏览器插件抓流**: 配套 Chrome/Edge 扩展嗅探网页上的 HLS(m3u8)、TS 分片与直链视频，可在浏览器内直接下载（含 AES-128 解密、多码率选择、断点续传），也可一键推给桌面端由 ffmpeg 下载并入库；桥接只绑本机环回地址且必须配对令牌。
- **手机端浏览**: 同一局域网的手机浏览器可刷本机视频与图片；有开关与可选访问 PIN，收藏与点赞和桌面端是同一份数据。
- **现代化 UI**: 基于 Vue 3 的视频工作台，主列表支持持续加载和虚拟化，网格适合视觉浏览。
- **右键菜单**: 快速播放、定位文件、重命名或安全删除记录。

## 技术栈

- **后端**: Go + GORM
- **前端**: Vue 3 + Vite
- **框架**: Wails v2
- **数据库**: SQLite（默认）或 PostgreSQL（可选）

## 开发环境要求

- Go 1.23+
- Node.js 20.19+（20.x）、22.13+（22.x）或 24+
- Wails CLI v2
- PostgreSQL 12+（可选，仅在选用 PostgreSQL 后端时需要；语义搜索需要它和 `pgvector`）

## 安装依赖

```bash
# 安装 Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 进入项目目录（仓库根目录）
cd CineInsight

# 安装 Go 依赖
go mod download

# 安装前端依赖
cd frontend && npm install && cd ..
```

## 开发模式运行

```bash
export PATH=$PATH:$HOME/go/bin
GOTOOLCHAIN=go1.24.9 wails dev
```

## 构建生产版本

```bash
# 构建桌面应用
export PATH=$PATH:$HOME/go/bin
GOTOOLCHAIN=go1.24.9 wails build

# 构建产物位于: build/bin/
```

当前项目使用 Wails 2.11.0；本机默认 Go 1.27.1 会使其旧版 `go/packages` 在构建扫描阶段报 `package "fmt" without types`。上述命令只为本次 Wails 进程选择已验证的 Go 1.24.9，不修改全局 Go 设置。一键安装脚本在未显式指定工具链时也会自动使用 Go 1.24.9。

### macOS 一键打包并替换旧应用

```bash
# 构建并替换 /Applications/析微影策.app
bash scripts/build_and_install_app.sh

# 只构建，不替换已安装的应用
bash scripts/build_and_install_app.sh --build-only

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

## 平台支持

三个平台都能构建和运行，但下面这几项能力目前只在 macOS 上可用。其他平台不会崩溃，对应入口会返回明确的不可用原因。

| 能力 | 可用平台 | 其他平台上的表现 |
| --- | --- | --- |
| HEIC / RAW 图片解码与缩略图 | 仅 macOS | 解码依赖系统自带的 `sips` 命令。非 macOS 上返回「不支持解码」，预览请求为 404，图片仍会入库但没有可用预览 |
| IINA 播放断点回读与续播 | 仅 macOS | 进度来自 IINA 的 `~/Library/Application Support/com.colliderli.iina/watch_later`，这是 macOS 专属路径。其他平台读不到断点，外部播放只记播放次数 |
| 视频超分 | 仅 Apple Silicon macOS | 超分 sidecar 只随 Apple Silicon 构建打包。其他平台的能力探测返回 `platform_unsupported`，入口不可用 |
| 字幕依赖自动下载 | 仅 macOS | 自动安装 FFmpeg 与 Whisper 运行时走 Homebrew。Windows 上没有自动下载实现，会明确报「当前平台不支持自动下载字幕依赖」，需先手工安装再使用字幕功能 |
| 后台任务空闲判定 | 仅 macOS | 空闲与供电状态读 `ioreg` 与 `pmset`。其他平台一律视为始终空闲，等同于关掉「空闲时才跑自动后台任务」——自动任务照常立即执行 |
| 桌面通知与 Dock 角标 | 仅 macOS | 通知走 `NSUserNotificationCenter`，角标走 Dock 图标。其他平台是空实现，设置页的「桌面通知」开关不产生任何效果，应用内提示不受影响 |
| 删除原文件移到废纸篓 | 仅 macOS | 走系统废纸篓（`NSFileManager`）。其他平台一律按「该磁盘不支持废纸篓」处理：记录不动，由你在「永久删除（二次确认）」与「只从片库移除」之间选择 |
| 手机端二维码 | 仅 macOS | 二维码由 CoreImage 生成。其他平台设置页不显示二维码，手动输入页面上列出的局域网地址即可 |
| 人脸识别 | 仅 Apple Silicon macOS | 人脸检测与向量走托管 Python sidecar（onnxruntime + InsightFace），依赖与模型只为 Apple Silicon 固定了版本。其他平台的运行时状态返回 `incompatible`，设置页「人脸识别」分区的准备与分析入口都不可用；人脸向量与裁剪小图始终只在本机 |

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
10. **按条件随机**: 选择均衡、仅未看或仅收藏模式，在当前筛选范围内随机播放；不想看这一部时 30 秒内点「换一个」
11. **播放/打开**: 点击“播放”使用默认播放器，点击“打开目录”查看文件位置
12. **安全清理**: 在清理候选中预览重复与同源版本，确认后移入回收站
13. **备份与恢复**: 在设置页「数据库备份」配置备份目录、间隔与保留份数；恢复前应用会先为当前数据库创建安全备份（见下文「备份与恢复」）
14. **构建语义索引**: 为 AI 接口配置支持 `/embeddings` 的模型，在设置页显式开始构建；完成后可切换“语义搜索”或在详情中点击“找相似”
15. **查看片库洞察**: 使用顶部“洞察”入口查看容量、观看和标签分布

## 数据存储

结构化数据默认存储在本机 SQLite 数据库 `~/.CineInsight/library.db` 中，开箱即用，不需要任何配置（`SQLITE_PATH` 可改库文件位置）。头像和作品集封面复制到 `~/.CineInsight/media-details/`，不依赖原始图片路径。

选用 PostgreSQL 有两种方式：

- 在 `.env` 里写 `DB_BACKEND=postgres` 与下面的 `PG_*` 连接项。已经配置过 `PG_HOST`、但没写 `DB_BACKEND` 的老安装会自动沿用 PostgreSQL；
- 在设置页「数据库」分区检查目标库后「迁移并切换」，重启后生效；之后也可以「切回之前的后端（只改配置）」。切换写在 `~/.CineInsight/.env` 里；如果 `DB_BACKEND` 来自进程环境变量，应用内切换不可用。

PostgreSQL 的示例 `.env`：

```bash
DB_BACKEND=postgres
PG_HOST=127.0.0.1
PG_PORT=5432
PG_USER=video
PG_PASSWORD=your_password
PG_DB=video_master
PG_SSLMODE=disable
PG_TIMEZONE=Asia/Shanghai
```

语义搜索只在 PostgreSQL 后端可用，需要安装 `pgvector` 扩展，并要求当前数据库用户可执行 `CREATE EXTENSION vector`（或由管理员预先安装）。SQLite 后端或扩展不可用时只有语义功能不可用，片库其他能力不受影响。

### 备份与恢复

在设置页「数据库备份」分区操作，两个后端都支持：SQLite 用 `VACUUM INTO` 写一份 `.sqlite` 快照，PostgreSQL 用 `pg_dump` 写 `.dump`（需要本机装有 `pg_dump` / `pg_restore`）。默认目录 `~/.CineInsight/backups`，可以改，也可以「在访达中显示」；默认每 24 小时一份、保留 7 份，应用运行期间每小时检查一次是否到期，间隔填 0 关闭自动备份。「从备份恢复」会先给当前库做一份安全备份，完成后应用自动退出，重新打开即可。视频原文件不在备份里，需要自己按目录备份。

### 旧版数据迁移

`cmd/migrate_sqlite_to_pg` 只用于 PostgreSQL 之前那一代的旧库文件（`~/.CineInsight/video-master.db`）；当前的 SQLite 与 PostgreSQL 之间请用设置页的「迁移并切换」。如需迁移旧库，可运行：

```bash
go run ./cmd/migrate_sqlite_to_pg
# 或指定 sqlite 路径
go run ./cmd/migrate_sqlite_to_pg --sqlite ~/.CineInsight/video-master.db
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
