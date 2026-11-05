---
schema: loopx-plan/v1
source: 2026-11-05 当前会话：用户认可五项体验优化、手机图片5秒/视频播完自动下一项及预加载方案，并明确同意开始
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
---
# 自动浏览与体验优化

## Goal And Boundaries

批准来源为当前用户会话。手机端主动开启自动浏览，图片成功显示后停留5秒，视频结束前进；手动切换继续自动，暂停/面板/放大期间不前进，后台返回需主动继续。只准备当前及下一项，图片解码、视频首帧缓冲完成后复用元素；下一项未就绪保留当前画面并提示，失败/耗尽停留供重试或手动切换。当前播放优先，换范围清除旧预取。原手动模式、筛选、收藏范围、统计阈值、鉴权/后端合同保持。

同时落实认可的大图加载反馈、图片成功显示后记录查看、管理预览跨批次翻页、弹窗Tab循环及页面代码按需加载。保留先前未提交的图片留白关闭工作。不提交、推送或安装。

## P-001 手机自动浏览与预加载

四种媒体组合均通过准备态再切换，不重建已预取的元素；最多两个媒体，只有当前项能播放和触发观看统计。自动模式默认关闭，具有可见开关、暂停/继续及图片倒计时。重复结束事件、晚到响应、切筛选、关闭/后台、预取失败均不会错误前进。真实WebKit使用合成图片/视频验证复用和切换，手机实机性能仍须如实说明。

> writes: `frontend/src/short-feed/`, `frontend/src/components/*Feed*.test.js`, `frontend/scripts/`, `scripts/test_*webkit.sh`, `docs/short-feed-lan.md`
> anchors: 手机自动浏览、图片5秒、视频结束前进、预加载流畅、暂停和错误处理
> architecture: ShortFeedApp继续持有列表、当前项、观看记录和自动节奏；FeedStage继续唯一持有媒体元素及准备状态，仅向宿主报告就绪/错误，不新增服务或持久化源；静态/组件/真实WebKit验证生命周期。
> verify: `npx vitest run src/components/ShortFeed*.test.js src/components/FeedStage.test.js --maxWorkers=1`；手机合成WebKit验收。

## P-002 桌面图片加载与连续浏览

大图未完成时显示缩略图及加载提示，原图成功才启动查看计时；失败、隐藏、子窗口和切换清理计时。管理预览在末项且有下一页时继续加载，晚到响应不改变已关闭或已切换的预览。

> writes: `frontend/src/components/ImageViewport.vue`, `frontend/src/components/PhotoLibraryPage*`, `frontend/src/components/viewer/PhotoLightbox*`, `frontend/src/utils/`, `docs/context/ui.md`
> anchors: 大图加载反馈、准确计时、连续翻页
> architecture: 扩展ImageViewport显示态，列表和查看记录仍在各自页面，复用现有loadMore与代际取消，不共享新增状态。
> verify: 图片组件及其调用方测试；既有image_utility WebKit。

## P-003 弹窗焦点及页面延迟加载

最上层弹窗Tab/Shift+Tab循环，隐藏和禁用控件不参与；保留Esc和关闭焦点恢复。对非启动必需页面按需加载，不破坏常驻片库命令注册和导航请求。比较构建主包体积，不把包减小当作实际启动耗时改善。

> writes: `frontend/src/components/ui/BaseModal*`, `frontend/src/App.vue`, `frontend/src/*test.js`, `frontend/src/utils/`, `frontend/src/components/ui/*test.js`, `docs/context/ui.md`
> anchors: 弹窗键盘焦点、启动资源拆分
> architecture: BaseModal复用既有modalSignals栈，App保留页面所有权及常驻宿主；异步组件仅改变资源加载时机。
> verify: 弹窗与App导航测试、全前端测试和静态守卫、`npm run build`。

## Integration And Final Verification

资源重任务串行、nice 10；前端全套和所有静态脚本、构建、相关WebKit。保留已知RecapPage月份夹具断言失败并单列；不改无关业务。检查实际diff与原始请求，核对双媒体释放、晚到请求、准备态不记观看、暂停和导航行为。

## Handoff And Residual Risks

作者自查：现有模块内扩展，无后端/API/数据库、安全或破坏性变更，不需要独立审批。基线：上轮前端3347通过/1既有Recap月份失败；本轮手机/弹窗基线见 /tmp/cine-mobile-baseline.log。尚未手机实机或安装验证；本机WebKit不等同于iOS Safari/Android真机性能。


## 实施与验证记录（2026-11-05）

- P-001：自动浏览默认关，图片decode成功后5秒，video ended切换；操作/后台暂停、失败重试、下一项未准备好保留当前画面。媒体元素按资源身份复用，当前+下一项，历史40项；换范围/退出中止请求并用代际丢弃晚到结果。最终手机专项16项通过；随后加强晚到响应mock（带真实JSON头并断言signal已中止），该文件12项再次通过。
- P-002：原图成功显示后启动查看计时；等待时缩略图与提示，管理侧预览跨页及关闭重开代际保护。图片/模态相关148项通过，随后增加跨页晚到响应场景随回归通过。
- P-003：栈顶模态Tab循环，页面异步加载。App的shallow测试按实际异步页props生成替身以保留原导航断言；App与手机专项合计236项通过。片库命令宿主仍常驻，无后端改动。
- 全前端组件回归：3360通过、1失败（RecapPage.test.js既有月份断言：夹具固定2026-10，断言取当前2026-11）。之后补了“失败预取被手动切到当前项”的回归与处理，最终相关16项通过；未把后续专项结果说成重新跑过全套。
- 16组静态脚本全部通过。short-feed静态守卫随双槽模板更新，仍检查预取档位/当前缓冲通知/请求筛选合同；新按钮使用统一样式和设计token，没有加入例外名单。
- 最终`npm run build`成功。主入口JS从2563.40 kB降至1368.47 kB（约46.6%），gzip从721.21 kB降至393.87 kB。原500 kB包体提醒仍存在；没有修改阈值。没有实测应用启动耗时。
- `bash scripts/test_auto_browse_webkit.sh`最终通过：390px宽、四种图片/视频组合、真实ended、5秒停留、预取DOM复用、暂停、45次切换后媒体/准备状态≤2和历史≤40、退出释放、按钮无横向溢出。非法两个参数返回2。只使用合成素材。
- `bash scripts/test_image_utility_webkit.sh`通过：21组既有图片/编辑/对比/增强场景及本轮留白点击/图片点击/拖动断言。未安装桌面应用、未在iOS/Android实机验收；节点数量有界不等于已测手机实际内存、耗电或弱网帧率。
- 在临时副本进行定向变异验证，工作区源码字节保持：图片5秒推进、准备态门、预取元素身份、图片加载后计时、管理跨页、Tab循环、晚到预取代际共7个变异均被测试检出（6+1分两轮）。无存活变异；非覆盖全项目的变异测试。
- 日志位于本机`/tmp/cine-final-*.log`、`/tmp/cine-static-final.log`、`/tmp/cine-mutations.log`、`/tmp/cine-stale-mutation.log`。最终diff检查通过。所有修改未提交、未推送、未安装；无其他已知相关失败。
