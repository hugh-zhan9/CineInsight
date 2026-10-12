---
schema: loopx-plan/v1
source: docs/loopx/design/2026-10-12-apple-subtitles/概要设计.md
status: complete
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: [P-001]
  - id: P-003
    status: done
    depends: [P-002]
---
# 系统字幕替换

## Goal And Boundaries

落实[用户需求](../../../.loopx/intake/2026-10-12-apple-subtitles/requirements.md)与[设计](../design/2026-10-12-apple-subtitles/概要设计.md)，直接替换字幕第三方 AI，保留手选与自动语言、文件保护和历史术语。其余 AI 仅验证。无提交、安装或部署授权。

## P-001 原生系统桥

提供系统能力检查、识别语言准备、手选/自动检测、词级时间片段、批量翻译及系统语言包准备；本机原生调用与失败边界可重复验证。

> writes: `native/apple-ai/**`, `scripts/build_apple_ai.sh`, `services/apple_ai*.go`
> anchors: AC-01/02/05, D-001/002, TC-01/02/06
> architecture: 原生桥从属于字幕服务；每请求子进程，不持有数据库或字幕写回责任。
> verify: Swift 编译及原生探测；Go 桥协议/取消测试。

## P-002 字幕调用方和界面迁移

删除字幕第三方执行/准备路径，替换设置与生成/翻译/工作台界面；保留其他 AI 的共享运行时工具，保留旧数据和 pending 产物。

> writes: 字幕后端及直接调用方、设置/字幕/作品集界面与测试、Wails生成绑定、打包脚本
> anchors: AC-01/02/03/04/05, D-001/002/003/004, TC-03/04/05/06
> architecture: 复用 SubtitleService 队列/写入器、现有翻译接口，不增任务 owner。
> verify: 受影响包/调用方、前端组件及脚本、绑定生成。
> review: 独立审查最终diff的取消/文件保护/历史任务兼容。

## P-003 集成、其他能力报告与文档

完成整包与回归验证，更新当前上下文，记录其他四项可行性证据及限制。

> writes: `AI-CONTEXT.md`、本次设计/计划、必要集成修正
> anchors: 全部 AC/TC, D-005
> architecture: 只补证据与集成，不借此扩展其他 AI 实现。
> verify: go test ./...；相关 race；npm test；npm run build；Wails生成与build-only；独立最终审查。

## Integration And Final Verification

对照原始需求，验证自动结果可改、语言缺失不外发、取消原文件不动；检查最终 diff 不误删共享 Python 工具。文档链接、旧历史与构建状态如实记录。

## Handoff And Residual Risks

已完成。证据见[最终验证记录](../design/2026-10-12-apple-subtitles/需求设计文档.md#最终验证记录)。原生 smoke、services 全包、相关 race、前端全量、vet、打包和独立复审通过；Go 根包的图片目录用例在干净 HEAD 副本也存在间歇失败，未扩修。真实影片质量与新语言首次下载 UI 未做人工验收。产物已构建但未安装，其他 AI 未替换，未提交。
