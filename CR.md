# Video Master — 代码审查报告

> 审查时间：2026-02-13 | 审查范围：全部后端 Go 代码 + 前端 Vue 代码

---

## 总览评分

| 维度 | 评级 | 说明 |
|------|------|------|
| 功能完整性 | ⭐⭐⭐⭐ | 核心功能齐全，需求文档中大部分已完成 |
| 代码可读性 | ⭐⭐⭐⭐ | Go 代码清晰；Vue 单文件过大但逻辑清楚 |
| 架构设计 | ⭐⭐⭐ | 分层合理，但前端缺乏组件拆分 |
| 安全性 | ⭐⭐⭐ | 存在几处值得注意的问题 |
| 测试覆盖 | ⭐⭐⭐ | 有基础测试，但覆盖面不足 |
| 工程规范 | ⭐⭐ | 缺少 linter、CI/CD、`.gitignore` 不完整 |

---

## 🔴 严重问题

### ~~1. 日志文件句柄泄漏~~ ✅ 已修复

**文件**: `app.go` L59-L61

`setLogEnabled` 每次调用都打开新的文件句柄，但**从未关闭旧句柄**。`GetSettings()` 每次被前端调用都会触发 `setLogEnabled`（第 217 行），每次前端获取设置就多泄漏一个文件描述符。

```go
// 问题代码
if f, err := os.OpenFile(logPath, ...); err == nil {
    log.SetOutput(f)   // 旧的 os.File 被丢弃，永不关闭
}
```

> ⚠️ 长时间运行会耗尽文件描述符。应保存旧 writer 并在切换前关闭。

---

### ~~2. `rand.Seed()` 已废弃~~ ✅ 已修复

**文件**: `services/video_service.go` L366

Go 1.20+ 中 `rand.Seed()` 已被废弃，全局 `rand` 默认已自动 seed。此处在每次 `PlayRandomVideo()` 调用中都重新 seed，反而可能**降低随机性**（高速调用时 `UnixNano` 可能相同）。

```go
rand.Seed(time.Now().UnixNano())  // 不再需要，删除即可
```

---

### ~~3. `PlayRandomVideo` 全量加载所有视频到内存~~ ✅ 已修复

**文件**: `services/video_service.go` L316

随机播放每次 `Find(&videos)` 加载**全部视频**（包括 Tags 预加载）。当视频量达数万时，会严重消耗内存。

> 建议改为数据库端计算分数排序 + 分层随机策略，或至少 `Select` 必要字段而非全部列。

---

### ~~4. 游标分页 SQL 参数绑定维护困难~~ ✅ 已修复

**文件**: `services/video_service.go` L48-L52

`scoreSQL` 中有 `?` 占位符，在 `WHERE` 子句中被重复内嵌了 **3 次**，每次都需要传入 `playWeight`。如果参数顺序计数出错，SQL 结果将完全错误。

```go
query.Where("("+scoreSQL+" > ?) OR ("+scoreSQL+" = ? AND size < ?) OR ("+scoreSQL+" = ? AND size = ? AND id < ?)",
    playWeight, cursorScore,
    playWeight, cursorScore, cursorSize,
    playWeight, cursorScore, cursorSize, cursorID,
)
```

> 当前参数数量刚好正确（9 个 `?` 对应 9 个参数），但极难维护。建议提取为命名子查询或构建更清晰的 SQL。

---

### ~~5. `database.Init()` 中的冗余目录逻辑~~ ✅ 已修复

**文件**: `database/database.go` L25-L26

`dataDir` 和 `legacyDir` 完全相同（都是 `~/.video-master`），整个 legacy 判断逻辑**永远不会生效**：

```go
dataDir := filepath.Join(homeDir, ".video-master")
legacyDir := filepath.Join(homeDir, ".video-master")  // 与 dataDir 相同！
```

同样的问题也出现在 `app.go` L49-L50 的 `setLogEnabled` 中。

---

## 🟡 架构与设计问题

### ~~6. 前端 App.vue 巨大单文件（1743 行 / 45KB）~~ ✅ 已修复

**文件**: `frontend/src/App.vue`

包含**所有 UI**（视频列表、设置页、6 个弹窗、700+行 CSS）。违反了 Vue 组件化最佳实践。

> 建议至少拆分为：`VideoList.vue`、`SettingsPage.vue`、`TagManager.vue`、`ScanDialog.vue`、`DeleteConfirmDialog.vue`、`DirectoryManager.vue`

---


### ~~9. `UpdateSettings` 参数过多~~ ✅ 已修复

**文件**: `services/settings_service.go` L18

接受 6 个独立参数，每次添加新设置项都要修改签名。建议改为接受 `models.Settings` 结构体。

---

## 🟠 代码质量问题

### ~~10. 搜索输入无防抖~~ ✅ 无需修复（桌面单体应用 IPC 通信快，不需要防抖）

**文件**: `frontend/src/App.vue` L26-L27

搜索框 `@input="handleSearch"` **每次按键都触发**后端查询，缺少 debounce，高频输入时会产生大量请求。

---

### ~~11. `openFileManager` 和 `openWithDefault` 代码重复~~ ✅ 已修复

**文件**: `services/video_service.go` L403-L436

两个函数几乎相同（macOS/Linux 完全一致），仅 Windows 参数不同。应合并为一个函数。

---

### ~~12. `PlayVideo` 存在竞态条件~~ ✅ 已修复

**文件**: `services/video_service.go` L293-L297

先读取 `video.PlayCount` 再 +1 写入，非原子操作。并发场景下可能丢失计数。应使用 `gorm.Expr("play_count + 1")` 在数据库端原子递增。

---

### ~~13. `AddVideo` 多余的逻辑分支~~ ✅ 已修复

**文件**: `services/video_service.go` L144-L146

`if err == nil` 在 `if err != nil { return }` 之后总是 true，属于多余检查。

---

### ~~14. 前端 `video.tags` 可能为 `null`~~ ✅ 已修复

Vue 模板中 `video.tags.map(t => t.id)` 等调用假设 `tags` 始终是数组。如果后端返回 `null`（JSON `"tags": null`），将产生运行时错误。Go 中 `Video.Tags` 为空 slice 时 JSON 会序列化为 `null` 而非 `[]`。

---

### 15. 开发依赖版本过旧

**文件**: `frontend/package.json`

Vite `3.0.7` 和 `@vitejs/plugin-vue` `3.0.3` 均已过时多个大版本（当前最新 Vite 6.x）。存在已知安全漏洞风险。

---

## 🔵 工程规范问题

### ~~20. 缺少错误类型定义~~ ✅ 已修复

后端使用 `errors.New("视频已存在")` 和 `errors.New("TAG_EXISTS")` 混合了中文和英文错误码。建议定义统一的错误类型（sentinel errors 或 error codes），便于前端可靠判断。

---

## ✅ 做得好的地方

| 亮点 | 位置 |
|------|------|
| 游标分页设计合理 | `video_service.go` 三元组 `(score, size, id)` 排序 |
| 智能随机播放算法文档完善 | `ALGORITHM.md` 详细解释加权公式 |
| 增量扫描 + 清理陈旧记录 | `App.vue` 前端对比扫描结果与数据库 |
| 视频路径唯一约束 + 重复清理 | `database.go` 启动时自动清理 |
| 测试可替换函数指针 | `openWithDefaultFn` 方便测试注入 |
| 跨平台兼容 | `runtime.GOOS` 路由到不同系统命令 |

---

## 优先修复建议

| 优先级 | 问题编号 | 行动 |
|--------|---------|------|
| **P0** | #1 | 修复日志文件句柄泄漏 |
| **P0** | #2 | 删除已废弃的 `rand.Seed()` |
| **P1** | #5 | 清理冗余的 legacy 目录逻辑 |
| **P1** | #10 | 为搜索添加 300ms debounce |
| **P1** | #12 | 播放计数改为数据库原子操作 |
| **P1** | #16 | 完善 `.gitignore` |
| **P2** | #6 | 拆分 `App.vue` 为多个组件 |
| **P2** | #14 | 处理 `tags` 为 `null` 的情况 |
| **P2** | #15 | 升级前端依赖 |
| **P3** | #9, #11, #13 | 代码清理与重构 |
