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
