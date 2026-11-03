---
schema: loopx-plan/v1
source: docs/loopx/design/2026-10-30-prowlarr-download/需求设计文档.md#D-PT-10
status: blocked
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: [P-001]
  - id: P-003
    status: blocked
    depends: [P-002]
---

# PT 原站详情读取

## Goal And Boundaries

2026-11-03 用户明确接受应用内原站详情及必要站点鉴权。按 D-PT-10，修正原先仅展示列表字段的实现；不改变搜索、下载选择、任务归属或入库，凭据不出后端，不另建账号库。不自动提交或推送本轮变更。

## P-001 后端详情与截图

令牌绑定 Prowlarr 站点配置，适配当前 NicePT API/Cookie；有界读取、纯文本清洗、原安全图片代理及维护取消。未知站点如实不支持。已完成基线：相关 Go TestBT、前端84项通过；本机 Prowlarr 未运行，直接站点只读探测403，不能宣称真实正文已验。

> writes: `services/bt*.go`, `services/btclient/*.go`, `app_bt*.go`, `preview_asset_handler.go`
> anchors: D-PT-10
> architecture: 扩展BT搜索的短期令牌，不增加存储；站点鉴权只归后端；外站IO不持命令门闩
> verify: agent-go test ./services ./services/btclient . -run 'TestBT|TestSiteDetail' -count=1 -timeout 300s
> review: 来源与凭据绑定、SSRF、正文内容和代际隔离

## P-002 前端接线

生成Wails绑定，在详情页加载正文、截图、MediaInfo，处理错误、显式重试和迟到响应；原文件选择流程保持。

> writes: `frontend/src/components/bt/*`, `frontend/src/utils/btAPI.js`, `frontend/wailsjs/**`
> anchors: D-PT-10
> architecture: UI → BT facade → 现有协调模块；展示状态属于详情组件
> verify: 单worker相关Vitest、绑定守卫、前端build

## P-003 集成验收与安全复核

检查实际来源和剩余限制，独立审阅精确diff，修复重要问题后复审；更新现有验证记录。

> writes: `docs/context/integrations.md`, `docs/loopx/design/2026-10-30-prowlarr-download/验证记录.md`
> anchors: D-PT-10
> architecture: 本地合成验证与真实站点证据分别记录
> verify: 受影响包与调用方测试、静态守卫、build、隔离定向变异
> review: 安全相关完整diff独立审阅

## Integration And Final Verification

本机重编译串行经agent-go；生成绑定同资源预算。保留既有frontend/package.json.md5改动。代码、来源或环境变更后重跑受影响检查。

## Handoff And Residual Risks

真实站点需要有效的Prowlarr及站点授权。基线命令成功不代表新功能或真实站点通过。代码、绑定、回归与安全复审已完成。真实站点曾直接返回正文5747字符，协议结构已核实；最终生产读取链路因当前NicePT鉴权API超时未通过，不得标记真实验收完成。


## 当前执行证据

- P-001/P-002：已完成，详情请求、原站适配、纯文本/截图代理、业务状态与资源身份验证、维护/代际隔离已接线。
- P-003：独立复审已无 Critical / Important；4项隔离变异均检出；全量Go通过。前端完整组件运行3149项，修复资源列表按钮样式守卫后，剩余1项RecapPage月份断言在未修改HEAD副本中同样失败；全部静态脚本与构建通过。最终聚焦检查见同设计目录验证记录。
- 用户另行明确批准NicePT DNS与两条精确公网IP代理路由。已在Shadowrocket当前配置添加nicept.net、*.nicept.net的总是真实IP条目，及104.21.21.215/32、172.67.200.95/32两条PROXY/no-resolve路由；配置UI确认规则668→670，固定IP的HTTPS验证成功。未改默认路由。
- 仍未解决：NicePT鉴权API请求当前超时，Prowlarr资源搜索无法返回有效列表，最终生产方法只读探针失败；真实截图与应用WKWebView验收未完成。已支持明确错误，不扩大超时、关闭校验或加入自动重试。未提交、推送、打包或安装本轮代码。
