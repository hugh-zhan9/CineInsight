# AI_CHANGELOG

## 2026-03-05 13:33:00
- `Change`: 修复设置页面的视频支持格式文本框宽度没有占满的问题，以及全局深色模式背景颜色没有应用到 `<html>` 和 `<body>` 上的问题。
- `Risk Analysis`: 此修改仅涉及纯UI样式的调整和 Vue 内部系统主题绑定逻辑，将 data-theme 从 #app 提升到了 document.documentElement，副作用极低，不会影响其他功能模块。
- `Risk Level`: S3-低
- `Changed Files`: `frontend/src/App.vue`, `frontend/src/components/SettingsPage.vue`

## 2026-03-05 13:37:00
- `Change`: 移除视频列表顶部标签过滤区的关闭（×）图标的白色背景（由于缺乏样式默认渲染了底层 button 背景）。
- `Risk Analysis`: 仅在全局 CSS 中给 `.tag-chip-delete` 补充了透明背景和无边框样式。零风险。
- `Risk Level`: S3-低
- `Changed Files`: `frontend/src/App.vue`
## [2026-03-11 11:19] [Bugfix]
- **Change**: 修复 go test 基线失败（补齐前端 dist 并更新测试签名）
- **Risk Analysis**: 涉及 .gitignore 与测试用例调整，风险在于潜在忽略规则影响构建产物或测试参数变化导致覆盖不足
- **Risk Level**: S2（中级: 局部功能异常、可绕过但影响效率）
- **Changed Files**:
- `frontend/dist/.keep`
- `.gitignore`
- `services/video_service_test.go`
----------------------------------------
## [2026-03-11 11:22] [Feature]
- **Change**: 切换运行时数据库到 Postgres 并新增连接配置
- **Risk Analysis**: 新增 .env 配置读取与 Postgres 连接；风险在于未配置环境变量时应用启动失败，需要文档提示
- **Risk Level**: S2（中级: 局部功能异常、可绕过但影响效率）
- **Changed Files**:
- `database/database.go`
- `database/database_test.go`
- `go.mod`
- `go.sum`
----------------------------------------
## [2026-03-11 11:25] [Feature]
- **Change**: 新增 sqlite→postgres 迁移 CLI 与数据快照加载
- **Risk Analysis**: 新增迁移工具与序列重置逻辑，风险在于迁移时 PG 非空或数据量大导致耗时，需要文档提醒
- **Risk Level**: S2（中级: 局部功能异常、可绕过但影响效率）
- **Changed Files**:
- `cmd/migrate_sqlite_to_pg/main.go`
- `cmd/migrate_sqlite_to_pg/main_test.go`
----------------------------------------
## [2026-03-11 11:29] [Bugfix]
- **Change**: 为 bindings 构建添加无数据库初始化入口
- **Risk Analysis**: 增加 bindings 构建主入口与共享 assets，风险在于绑定生成流程变化导致运行期未初始化数据库的路径被误用
- **Risk Level**: S2（中级: 局部功能异常、可绕过但影响效率）
- **Changed Files**:
- `main.go`
- `main_bindings.go`
- `assets.go`
----------------------------------------
