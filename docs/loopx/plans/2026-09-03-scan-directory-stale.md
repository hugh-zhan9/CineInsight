---
source: 用户裁决（2026-09-03，本文件《Goal And Boundaries》记录 D-S01..D-S04）
status: ready
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: []
  - id: P-003
    status: done
    depends: [P-002]
  - id: P-004
    status: done
    depends: []
  - id: P-005
    status: done
    depends: [P-001, P-002, P-003, P-004]
  - id: P-006
    status: done
    depends: [P-002, P-004]
---

# 删除扫描目录后的记录去向

## Goal And Boundaries

现状：删掉一个扫描目录只删 `scan_directories` 里那一行（`directory_service.go:39`），
`videos` / `images` 一个字节都没动；片库列表又不按扫描目录过滤，所以那批记录继续挂在
列表里。之后无论手动全量扫描还是实时同步，都只在**当前**扫描根之下对账
（`getActiveVideosUnderRoots`），已经移出清单的那批记录压根不进对账集合，于是既不会被
标失效也不会被清理——它们停在"扫描看不见、列表看得见"的夹缝里。

要达到的结果：删目录之后那批记录从列表里消失，但记录本身留着；把同一个路径加回来时，
数据自动回来，标签、评分、观看进度一并保住。图片同理。

用户已裁决、不再重开的四条：

- **D-S01 视频侧用标失效，不软删**。删目录时把该目录下的视频标成 `is_stale=true`，
  记录不软删、**不进回收站**。视频的软删走 `deleteVideoRecord`，每条都会建一个
  `VideoTrashEntry`（`video_service.go:457`），删一个目录就往回收站灌上千条，不能这么干。
- **D-S02 默认列表不再显示失效记录**，「路径失效」智能视图仍然能看到它们。这条顺带改变
  了既有表现：因播放失败或文件丢失而失效的记录，今天是显示在主列表里的，改完也会一起
  从主列表消失。用户已知悉并接受。
- **D-S03 加回目录时立即扫一遍该目录**，只有真的在盘上的文件才恢复，不无条件复活整批。
- **D-S04 图片达成同样的用户可见结果，但沿用图片侧既有机制**：软删 + `is_stale` 标记
  （`deleteMissingImageRecord`），路径回来时由 `restoreStaleImage` 复活。不把视频那套
  硬套过去——图片的软删本来就不建回收站条目，它那条路已经是对的。

全局约束：不改任何现有 Wails 导出方法签名与事件名；不动数据库结构（`is_stale` 两张表都
已经有了）；磁盘文件在整条链路上一个字节都不动；SQLite / Postgres 双后端都要过既有测试
基座；Claude 不提交、不推送。

非目标：给筛选菜单加"按路径筛选"；把失效记录做成可批量清理的入口；改回收站语义；
改「路径失效」视图的现有含义（它仍然是"这条记录当前指不到文件"，只是来源多了一种）。

## P-001 默认列表与随机播放排除失效记录

`applyLibraryFilter`（`services/library_service.go`）末尾那个 `switch filter.SmartView`
是主片库与随机播放共用的筛选边界。在它之外补一条：智能视图不是「路径失效」时，一律
只取 `is_stale = false`。

随机播放共用这条边界是有意的收益：失效记录指不到文件，随机抽中它只会得到一次播放失败。

完成的判据：默认视图查不到失效记录；「路径失效」视图只查得到失效记录；其余每个智能视图
都不会再返回失效记录；随机播放同样不会抽中它们。

> writes: `services/library_service.go`, `services/library_service_test.go`
> anchors: D-S02
> verify: `go test ./services/ -run LibraryDefaultViewsExcludeStale`

## P-002 删除扫描目录时把该目录下的记录标为失效

在 `VideoService` 上新增一个方法：给定被移除的根与剩余的根，把只属于被移除根的活跃视频
标成失效。路径归属沿用既有的 `videoBelongsToRoots`，不另造一套匹配规则。

要点是**嵌套目录**：一个视频可能同时落在被删的根与另一个仍在清单里的根之下（比如
`/media` 和 `/media/movies` 都配着，删掉 `/media`）。这种记录必须保持原样，不能标失效。

`App.DeleteDirectory` 在删配置行之前先标记；标记失败就不删配置行并把错误返回给用户——
删了配置又没标上，那批记录会再次掉进现在这个夹缝里。

完成的判据：删掉一个目录后，该目录下的视频全部 `is_stale=true` 且没有任何回收站条目产生；
同时落在其他仍配置的根之下的视频不受影响；磁盘文件没有变化。

> writes: `services/video_scan.go`, `app_settings.go`, `services/video_scan_test.go`
> anchors: D-S01
> verify: `go test ./services/ -run "MarkVideosStale"`
> review: 嵌套根的归属判断是否会误标；标记失败时配置行是否确实没被删

## P-003 加回目录时立即扫描恢复

`App.AddDirectory` 在建好配置行之后，对这一个目录跑一次窄扫描
（`SyncAffectedDirectories`，它在文件重新出现时会清 `is_stale`，见 `video_scan.go:413`）。

扫描放在后台跑并复用既有的 `library-watcher-reconciled` 事件通知前端刷新：添加目录的
对话框不该被一次可能几分钟的扫描卡住。恢复只发生在文件确实扫得到的记录上——盘没插、
路径写错时不会有任何记录被复活，这正是要的行为。

完成的判据：删掉目录再加回同一路径后，原有记录恢复为非失效并重新出现在默认列表里，
标签、评分、观看进度保持不变；路径写错时没有任何记录被复活。

> writes: `app_settings.go`, `app_test.go`
> anchors: D-S03
> verify: `go test ./ -run TestDeleteDirectoryMarksStaleAndAddBackRestores`；人工走一遍删除→加回

## P-004 图片侧对齐

`DeleteImageDirectory` 在删配置行之前，把只属于该目录的活跃图片走既有的
`deleteMissingImageRecord`（软删 + `is_stale`，不建回收站条目）。`AddImageDirectory`
之后触发一次图片扫描，由既有的 `restoreStaleImage` 复活它们。

图片侧只有全量的 `SyncImageDirectories()`，没有按目录的窄扫描，因此这里跑的是全量扫描；
不为这件事新造一个窄扫描入口。

完成的判据：删掉图片目录后该目录下的图片从图库消失、记录仍在库里；加回同一路径后自动
恢复且标签评分保持；嵌套目录下同时属于其他根的图片不受影响。

> writes: `services/image_service.go`, `app_image.go`, `services/image_service_test.go`
> anchors: D-S04
> verify: `go test ./services/ -run "MarkImagesStale"`
> review: 软删的图片是否会被别处当成"用户主动删除"而清掉恢复标记

## P-005 界面文案与文档

删除目录的确认框现在写的是「库里已有的视频记录不会被删除。」——改完之后这句话虽然仍然
成立，但会让人以为记录还留在列表里。改成说清楚新行为：记录会保留但从列表隐藏，加回同一
路径就会恢复。图片目录的删除确认同理。

同时更新 `AI-CONTEXT.md` 里扫描对账那一节，把"删目录 = 标失效 + 加回即恢复"这条口径写下来。

完成的判据：确认框文案与实际行为一致；文档写明这条口径与它对「路径失效」视图含义的扩展。

> writes: `frontend/src/components/settings/ScanDirectoriesSection.vue`, `frontend/src/components/SettingsPage.test.js`, `AI-CONTEXT.md`
> anchors: D-S01..D-S04
> verify: `cd frontend && npm test`

## P-006 对账收拾历史遗留的孤儿记录

P-002 / P-004 只在**删除目录那一刻**处理记录，修不了在此之前就已经掉进夹缝的那批——
而那正是用户最初看到的现象（图片目录早就删了，图片还在图库里）。补一条：全量对账时把
**不属于任何已配置目录**的记录一并处理，视频标失效、图片按失踪对账软删，与 D-S01 / D-S04
同口径。

判据用的是**已配置的目录**而不是本轮成功扫到的根：移动硬盘没插时那个根扫不了、进不了
`roots`，但它仍然配置着，底下的记录绝不能因此被隐藏。一个目录都没配置时所有记录都是孤儿、
全部隐藏——"没有扫描目录就不该有内容"。目录清单读失败时直接返回错误，绝不走到这一步，
否则会把整库藏起来。

完成的判据：库里存在不属于任何已配置目录的记录时，跑一次对账后它们从列表消失、记录仍在；
目录配置着但扫不到（盘没插）时底下的记录不受影响；把目录加回来后这批记录自动恢复。

> writes: `services/video_scan.go`, `services/image_service.go`, `services/scan_directory_stale_test.go`
> anchors: D-S01、D-S04（补历史遗留）
> verify: `go test ./services/ -run "Orphan|Unreachable"`
> review: 盘没插时会不会误隐藏；目录清单读失败的路径是否确实不会走到对账

## Integration And Final Verification

- `go build ./...`、`go test ./...` 全绿；新增测试在 SQLite 与 Postgres 双后端各跑一次。
- `cd frontend && npm test` 全绿。
- `gofmt -l $(git ls-files -co --exclude-standard '*.go' | grep -v '^search/')` 无输出。
- 端到端：删一个含数据的扫描目录 → 列表里消失、「路径失效」视图里看得到、回收站没有新条目
  → 把同一路径加回来 → 记录恢复且标签评分观看进度不变。图片同一遍。

## Handoff And Residual Risks

- Blockers: 无。
- Residual risks: D-S02 会让"因播放失败而失效"的记录也从主列表消失，这是用户已接受的
  连带变化；图片侧加回目录触发的是全量扫描，目录多时会比视频侧慢。
- Resume note: 进度以本文件 frontmatter 的 slice status 为准。
