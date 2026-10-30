---
schema: loopx-plan/v1
source: "2026-10-30 本轮用户明确要求五项优化和重打包权限修复"
status: complete
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: []
  - id: P-003
    status: done
    depends: []
  - id: P-004
    status: done
    depends: [P-002]
  - id: P-005
    status: done
    depends: []
  - id: P-006
    status: done
    depends: []
---

# 项目优化与打包身份

## Goal And Boundaries
完成用户接受的五项建议，并修复重打包身份不稳定问题。保留筛选、版本聚合、命令面板、滚动、选择与失败语义，不提交、部署或重置系统权限。现有 frontend/package.json.md5 修改归用户保留。用户已选择本机专用持久证书；权威裁定见 ../design/2026-10-30-local-signing/概要设计.md。

## P-001 固定 WebKit 验收

交付 固定 WebKit 验收，以 正常、失败、平台边界脚本检查及真实 WebKit 为验收。

> writes: `scripts/test_macos_webkit.sh、scripts/test_macos_webkit_runner.py、frontend/package.json、frontend/scripts/playback-queue-webkit/runner.m 与 fixture.js`
> anchors: 用户接受的第 1 项
> architecture: 复用现有四套临时 fixture；脚本串行、失败即停
> verify: 正常、失败、平台边界脚本检查及真实 WebKit

## P-002 隐藏页延迟查询

交付 隐藏页延迟查询，以 组件激活、隐藏事件、重新显示、失败测试 为验收。

> writes: `frontend/src/components/VideoListPage.vue 及测试`
> anchors: 用户接受的第 2 项
> architecture: 常驻宿主和命令注册保留；首次激活加载，隐藏刷新合并
> verify: 组件激活、隐藏事件、重新显示、失败测试

## P-003 作品集性能基线

交付 作品集性能基线，以 受影响 Go 测试及不同规模 benchmark 为验收。

> writes: `services/collection_progress*`
> anchors: 用户接受的第 3 项
> architecture: 既有服务查询与汇总口径；先测后改，不加缓存或新存储
> verify: 受影响 Go 测试及不同规模 benchmark

## P-004 片库状态拆分

交付 片库状态拆分，以 完整前端测试与构建 为验收。

> writes: `frontend/src/components/video-list/libraryQuery.js、librarySelection.js、VideoListPage.vue、frontend/scripts/*test.mjs 及组件测试`
> anchors: 用户接受的第 4 项
> architecture: 复用现有 mixin 边界，查询与选择独立，无新全局状态
> verify: 完整前端测试与构建

## P-005 当前上下文整理

交付 当前上下文整理，以 链接、内容保全及 diff 检查 为验收。

> writes: `AI-CONTEXT.md、docs/context、docs/archive/context`
> anchors: 用户接受的第 5 项
> architecture: 根文件保留权威入口；主题当前合同与显式历史分开
> verify: 链接、内容保全及 diff 检查

## P-006 稳定打包身份

交付 稳定打包身份，以 签名完整性、两次产物身份一致、独立安全审阅 为验收。

> writes: `scripts/build_and_install_app.sh、签名脚本及文档`
> anchors: 用户接受的第 6 项（新增权限问题）
> architecture: 复用 macOS codesign；不修改 TCC；专用钥匙串证书固定指纹，私钥不进仓库
> verify: 签名完整性、两次产物身份一致、独立安全审阅

## Integration And Final Verification

串行运行前端测试、构建、受影响 Go 服务与直接调用方测试，WebKit 验收；检查最终差异和文档引用。签名涉及安全身份，实施前与最终差异需独立审阅。

## Handoff And Residual Risks

基线前端 202 文件 / 3074 用例通过，Go 定向通过。新前端 3076 用例与所有静态守卫通过；首次延迟加载两条测试先红后绿。Go 切片容量隔离 mutation 被版本组测试杀死，前端部分全选 mutation 被四个用例杀死，均用隔离副本不修改当前源码。性能结果见 ../../context/performance.md。独立 signing_review 审阅设计和最终差异，发现超分摘要与符号链接两个 Important，均修复并经复审无剩余 Critical/Important；5 个真实/边界签名测试通过。本机身份已创建，未改信任/TCC。完整 Go、WebKit、最终真实出包继续验证。

原生验收补正：新增固定入口实际执行发现原生鼠标失败；诊断记录第三次点击 active=0/key=0。runner 发送输入前将测试窗口设为前台并有界等待，不修改播放器断言；fixture 保留点击/媒体状态失败诊断。Go 最终 services 与 App 包通过（159.125s / 21.740s）。

`npm run test:macos` 最终退出 0：virtual_window、viewing_notes、playback_queue、video_crop 四套均 passed=true。前两套原样验证，队列/裁剪复用修正后的 native runner。

## 最终交付证据

- 前端：202 个组件测试文件、3076 用例通过；完整 npm test 所含静态守卫均通过（抽取后路径守卫跟随新 owner，未删断言）。
- Go：`agent-go test ./services . -timeout 1500s`，services / App 均通过；SQLite 三档 benchmark 及隔离容量 mutation 见性能记录。未测 PostgreSQL 性能。
- macOS：`npm run test:macos` 四套真实 WKWebView 全部通过；runner 正常、失败即停、参数及平台边界 4 用例通过。
- 签名：6 个测试通过，涵盖两套不同二进制的主包/辅助包固定 DR、篡改、清单、符号链接、执行路径及 Wails 无 XML 声明 plist。独立复审无剩余 Critical/Important。
- 首次完整构建的绑定、前端、Go 编译成功，签名读取 Wails plist 失败；改用系统只读 plutil 后，`build_and_install_app.sh --build-only -s -skipbindings` 复用未变化的前端和绑定，再编译、打包、签名成功，最终 `codesign --verify --deep --strict` 退出 0。
- 真实产物：`build/bin/析微影策.app`。主包 DR 已由 cdhash 改为原 identifier + 固定 certificate leaf。未替换 /Applications，未重置系统权限或修改信任设置；首次切换可能仍须一次授权，实际 TCC 升级体验尚未验收。
- 文档：入口约 376 KB → 16 KB；7 个领域文件保留原编号，44 个本地 Markdown 目标检查通过，所有原长段落保留（明确失效规则归档）。`git diff --check` 通过。
- 保留原有 frontend/package.json.md5；Wails 仅产生 models.ts 空白副作用，已确认无语义变化并恢复本轮生成前内容。并行出现的 Prowlarr/BT 工作未修改。未提交、推送或安装。
