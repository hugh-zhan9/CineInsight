---
schema: loopx-plan/v1
source: docs/loopx/design/2026-10-30-prowlarr-download/需求设计文档.md
status: complete
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-006
    status: done
    depends: []
  - id: P-009
    status: done
    depends: []
  - id: P-007
    status: done
    depends: [P-001]
  - id: P-008
    status: done
    depends: [P-001]
  - id: P-002
    status: done
    depends: [P-001, P-006, P-007, P-009]
  - id: P-003
    status: done
    depends: [P-006, P-009]
  - id: P-010
    status: done
    depends: [P-008]
  - id: P-004
    status: done
    depends: [P-002, P-003, P-008, P-010]
  - id: P-011
    status: done
    depends: [P-001, P-007, P-008, P-010]
  - id: P-012
    status: done
    depends: [P-001, P-002, P-003, P-007]
  - id: P-005
    status: done
    depends: [P-004, P-011, P-012]
---
# Prowlarr 搜索、选择性下载与原位入库

## Goal And Boundaries

用户2026-10-30已确认Prowlarr+本机qBittorrent、下载前勾选文件/全选、完成原位入库保留目录与文件名，并明确要求方案完成后直接开发。以[概要](../design/2026-10-30-prowlarr-download/概要设计.md)及[详细合同](../design/2026-10-30-prowlarr-download/需求设计文档.md)为当前权威。代码实施以设计安全复核后的版本为准；选型不重新询问。未授权本轮提交/推送、真实PT资源下载或自动接管已有任务。

## P-001 可验证的本机连接与站点搜索

提供有界本机HTTP客户端、系统凭据存储、Prowlarr搜索/代理取种与qBittorrent能力探测。凭据不进入前端响应、日志或备份，搜索结果使用不透明令牌。用假HTTP服务覆盖认证、跨源重定向、响应上限和空/失败语义；已有本机qBittorrent仅作只读探测。

> writes: services/btclient/*.go, services/bt_credentials*.go, services/bt_search*.go
> anchors: D-PT-01/04/05, TC-PT-01/06
> architecture: 新BT协调域的无数据库客户端，复用现有App接线方向；不挤入HTTP/HLS进程型下载服务；测试以注入HTTP/凭据后端隔离外部副作用。
> verify: agent-go test ./services -run TestBT -count=1；有界HTTP失败与脱敏场景。
> review: 凭据边界与服务端返回URL的信任链。

## P-006 有界种子元信息与路径清单

从P-002提取无状态解析责任，与连接/凭据无共享写入。交付v1/v2/hybrid文件清单、原始info字节hash、manifest摘要及路径边界，不访问网络、数据库或文件系统内容。文件ID稳定但不冒充qBittorrent index。

> writes: services/btmanifest/*.go
> anchors: D-PT-05, TC-PT-02/03/06
> architecture: BT域的纯解析模块，由协调器调用；只依赖标准库和仓库已存在的Unicode能力，无独立存储或运行状态。
> verify: agent-go test ./services/btmanifest -count=1；坏输入、边界、v1/v2/hybrid和独立hash真值测试。
> review: 路径穿越、Unicode/大小写碰撞、长度/深度/文件数限制、身份哈希正确性。

## P-009 已集成的BT数据模型与迁移

从P007拆出已完成的稳定数据合同：BTConnection/BTTask/BTFile、父子约束、TaskEpoch字段、字符串文件身份/独立qB index、padding NULL路径键、来源快照和AllModels/迁移夹具。独立复核未发现模型Important，主workspace SQLite/PG模型与往返测试已通过；此完成不代表凭据/配置生命周期已完成。

> writes: models/bt*.go, models/schema.go, database/migrator测试夹具
> anchors: D-PT-07, TC-PT-04/09
> architecture: 唯一持久模型供协调器和本地路径策略共用；不依赖网络/凭据实现，无新的运行期写路径。
> verify: 已在主workspace完成SQLite与一次性PG相关模型/迁移检查，见P007集成记录。
> review: 已完成P007范围内模型只读独立复核，无Critical/Important。

## P-007 持久连接配置

从P002提取不依赖元信息解析的模型与连接配置责任。完成连接/任务/文件实体、约束、AllModels/迁移夹具，以及调用P001凭据管理器的连接配置CAS、活跃任务保护、动态数据库访问；不实现任务网络执行。

> writes: services/bt_connection*.go, services/bt_credentials*.go（JSON字段标签及审阅要求的维护/跨实例凭据保护）, services/bt_search.go及其测试（仅公开Size十进制字符串投影）
> anchors: D-PT-04/07, TC-PT-04/09
> architecture: 数据模型定义唯一持久事实与外键顺序，配置服务消费P001的不可变凭据引用；不依赖P006，不创建另一份任务运行状态。
> verify: agent-go相关模型/连接/迁移测试，SQLite/PG方言合同。
> review: 凭据引用CAS、活跃任务时配置变更、唯一约束及双库迁移。

## P-008 资源搜索与文件选择组件

从P004提取无Wails直接调用的纯UI组件，按已定义DTO/props/emits实现搜索结果、文件选择、任务列表及连接配置；由P004接线真实接口。不得修改App、DownloadsPage、SettingsPage或生成绑定。

> writes: frontend/src/components/bt/*.vue, frontend/src/components/bt/*.test.js, frontend/src/utils/bt*.js
> anchors: D-PT-01/04/08, TC-PT-01/02/08/10
> architecture: 组件只收脱敏DTO并发出明确用户操作，无网络、存储或凭据持久化；独立于服务实现，不与P007共享写文件。
> verify: Vitest新组件测试（串行maxWorkers=1）、设计主题/样式守卫。
> review: 文件选择范围、敏感输入生命周期与用户任务操作边界。

## P-002 文件选择、任务持久化及对账

覆盖元信息、选择、独立保存目录、添加停止任务、优先级核实、启动及超时对账；同hash外部任务不接管。复用P007交付的新表/配置存储；所有网络写在事务外，以持久意图、CAS及单worker协调。

> writes: services/bt_task*.go, services/bt_manifest*.go, services/btclient/qb*.go, services/bt_search.go及测试（仅后端令牌资源描述与预览接线所需扩展）, services/bt_connection.go及测试（同实例qB用户名凭据更新与Drain失败生命周期恢复），models/bt.go及migrator BT夹具（仅pending_action/unresolved_write/observation_epoch）
> anchors: D-PT-02/05/07, TC-PT-02/03/04/08/09
> architecture: qBittorrent是下载事实源，BT任务表持有归属/意图/文件选择；保护目录先提交再远端add；不引入数据库应用锁。
> verify: agent-go定向BT/模型/迁移测试；目标qBittorrent合成元数据能力验证；无真实站点下载。
> review: 添加竞争、结果不确定、恢复旧备份及元数据预览边界。

## P-003 原位入库与扫描保护

对全量扫描、目录监听、直接添加、恢复/搬家匹配统一完成资格；受保护路径是withheld而非missing。复用既有视频记录/软删/身份与查重逻辑；崩溃后按路径身份修复关联。

> writes: services/bt_import*.go, services/bt_path*.go, services/video_scan.go, services/video_service.go, services/scan_restore.go, services/video_rename_move.go（仅relocateVideo资格接线）及直接调用方测试，models/bt.go及database/migrator夹具（仅publication_state持久发布标记）
> anchors: D-PT-03/06/07, TC-PT-05/06/07/08/09
> architecture: BTPathPolicy只读本地状态，扫描器不反向访问网络；VideoService仍拥有库记录创建；外部变更前后复核，不把内部文件锁夸大为外部排他锁。
> verify: BT与全部相关扫描/恢复/监听测试；同目录完成/未完成交错及用户软删不复活。
> review: 缺失对账、路径逃逸、保护墓碑与未知namespace。

## P-010 下载区交互编排组件

从P004提取仅依赖P008与D-PT-08接口合同的前端编排：api函数prop注入，串起搜索/预览/勾选/任务详情/分页与控制，丢弃旧请求，不直接导入Wails或改页面导航。P004随后装配实际绑定、播放/设置事件及外层页面。因此该切片无需提前解锁P002/P003，也不把模拟接口通过当作后端集成完成。

> writes: frontend/src/components/bt/BTDownloadsPanel.vue, frontend/src/components/bt/BTDownloadsPanel.test.js
> anchors: D-PT-05/08, TC-PT-01/02/08/10
> architecture: 复用P008展示组件，只持有页面请求代际与可见DTO；api prop是App适配器，不创建下载事实源、凭据存储或网络客户端。
> verify: 已验证且内容未变的P008组件基线；新编排组件Vitest正常/失败/过期响应测试，串行资源窗口；不生成绑定，P004负责真实接口消费守卫。
> review: 旧响应、文件选择、确认动作及未入库播放边界；无独立后端权限实现。

## P-004 App与服务生命周期接线

装配BT服务、入库策略、完成查重、任务中心、维护/退出与Wails方法，生成绑定；P011按固定D-PT08参数接前端，两者在同次交付中完成，并由P005跑全量绑定消费守卫。

> writes: app_bt*.go, main.go（schema后、Wails前BT本地初始化）, app.go及维护/任务中心/退出接线与直接App测试，services/database_switch_service.go及测试（仅只改配置的目标公开身份准备hook，目标库不写）, services/bt_duplicate*.go、services/bt_task_restore*.go、services/bt_credentials_scope.go（仅共用SQLite公开身份编码）、services/bt_path*.go及bt_import.go（仅复用扫描范围规则实现planned namespace验证）、BTService必要查重/恢复/结构化路径原因接线，services/bt_connection.go与测试、btclient/prowlarr.go与测试（仅补D08连接测试版本/能力投影）、services/download_duplicate.go（仅抽取复用视图投影）、models/bt.go及迁移夹具（仅逐文件查重结果字段）, frontend/wailsjs生成文件，services/testdata/model_schema_golden.json（按新增BT表生成）及services/product_fix_media_lib_test.go（仅源码守卫排除隔离.worktrees）
> anchors: D-PT-01/04/08, TC-PT-01/02/08/10
> architecture: App仅装配服务与依赖，UI不拿密钥/取种URL，不执行客户端权限之外的外部任务管理。
> verify: 相关App/BT/查重测试与Wails绑定生成；全量前端绑定消费和build在P004/P011合并后的P005验证。
> review: 凭据提交、任务操作授权、关闭应用与维护恢复。

## P-011 页面与导航装配

装配已验证P008/P010组件到下载页、设置页、想看片目找资源；独立BT导航不依赖浏览器桥接，现有HTTP/HLS不变。基于已确定D-PT08方法签名、已主测P001/P007 DTO与P010 facade写前端调用；已集成P002的公开DTO/方法声明用于适配，当前P002审阅修复均为内部持久意图/生命周期/响应预算，不改变此接口。因此本UI切片依赖接口合同而非未完成运行实现，mock验证不充当后端验收；后端Wails生成由P004独占，两个隔离候选顺序集成后做最终绑定双向检查。

> writes: frontend/src/App.vue及utils/appCommands.js、taskCommands.js、btAPI.js、btFormat.js与相关测试, frontend/src/components/bt/*, DownloadsPage.vue, SettingsPage.vue及settings/相关组件, WatchlistPage.vue、viewer/ListsPage.vue、TaskCenterDrawer.vue、QuitConfirmDialog.vue、video-list/IncrementalScanBar.vue及相应测试
> anchors: D-PT-01/04/08, TC-PT-01/02/08/10
> architecture: 仅UI状态/导航/接口适配；不写生成绑定、服务、模型或数据库，不创建第二份下载事实源。P004独占生成与后端，api函数名/参数按D08及P010注释。
> verify: 受影响Vitest与设计守卫；合并P004生成绑定后由P005完成全量npm test/build及绑定消费守卫。
> review: 凭据表单、旧响应/对话框、已导入播放与现有下载行为；最终整体复核由P005负责。

## P-012 独立合成下载与原位入库验收

从P005提取已定的实际qB端到端验证，依赖已完成协调器/入库接口，不依赖P004仍修正的SQLite恢复别名边界。仅新增验证文件，生产App/服务不归此切片；隔离工作区与唯一重验证窗口，最终P005仍核对全部集成结果。

> writes: services/bt_task_integration_test.go，services/bt_task_qb_probe_test.go（仅自有native进程清理，禁止未核验端口HTTP shutdown）
> anchors: D-PT-02/03/05/06, TC-PT-02/05/07/10
> architecture: 复用已有临时qB profile/loopback helper、生产客户端/协调器/导入器与dbtest；fake Prowlarr仅替代站点来源，无新运行期边界。全部临时文件、进程、端口自有，不访问用户片库或真实任务。
> verify: 显式环境开关的真实合成torrent下载，选择前无负载、选择priority核对、仅完成所选视频入库、hash/路径不变、Stop本地协调后qB仍做种；主集成复跑。
> review: native fixture资源归属与是否真实经过生产路径，不能把fake qB当实际验证。

## P-005 整体验证与文档核对

对照用户原话完成端到端合成torrent验证、实际本机服务只读连接检查、完整diff独立审阅和相关回归。用户站点账号未配置时，真实站点验收如实标明未验证，不用假服务宣称已下载资源。

> writes: 本设计目录状态/验证记录、docs/context/integrations.md、library.md、metadata.md、ui.md、AI-CONTEXT.md（仅关联BT落点及过时设置分区描述）、必要回归测试
> anchors: D-PT-01..08, TC-PT-01..10
> architecture: 验证既有HTTP/HLS、片单保留、扫描与PT原位做种保护；不把集成测试造数扩成真实资源下载。
> verify: 按资源限制串行Go测试/生成/前端build；最后审查同状态的证据、实际退出码和未验证项。
> review: 完整集成diff安全/兼容性独立审阅，修复Important后复审。

## Integration And Final Verification

原产品选择、业务成功/失败用例及设计索引都要按文字行为核对。常驻服务安装和站点配置属于独立环境动作，不能从代码测试通过推导已部署。保留当前工作区其他会话改动，禁止git add全仓或覆盖用户修改。

## Handoff And Residual Risks

设计只读复核已修正3项Important并通过相关复核。P-001已开始：services/btclient提供本机有界HTTP传输、qB版本/会话及Prowlarr站点脱敏投影、单站搜索及同源代理取种；凭据Keychain、协调器令牌/聚合尚未完成，不能标记整个P-001完成。已有其他会话在改AI-CONTEXT、VideoListPage、构建签名及相关文件，后续接线前重读。Prowlarr未安装/无站点账号；qBittorrent前轮已只读确认本机8081/5.2.3/API2.15.1。无用户确认待决产品项；具体协议能力与迁移证据需要逐项建立。

连接层验证：相关浏览器下载/扫描基线通过；`agent-go test ./services/btclient -count=1`通过；隔离副本loopback拒绝与redirect拒绝变异均被检测。仅说明客户端局部能力，不代表P-001或整个功能完成。

P-001局部复核：独立只读客户端审阅无Critical/Important；localhost TLS主机名与代理查询白名单两项Minor已修复并测试。当前代码没有接入App或启动任何外部下载，系统凭据存储与后续切片仍待完成。

2026-10-30 执行恢复点：基线HEAD `ebbbaa63678df2e5df402b4c536728c76ac8cbee`，主工作区 `/Users/zhangyukun/project/CineInsight`；原有用户delta仅frontend/package.json.md5。完整本轮已有内容快照 `/tmp/cineinsight-pt-run-checkpoint/manifest.json`（计划本身为控制器记账例外）；P001候选在 `.worktrees/pt-download-run`，原P001基线 `/tmp/cineinsight-pt-p001-base`。已派叶子worker完成P001，尚未集成；其余切片未解锁。

P-006已提取为独立纯解析切片，候选工作区 `.worktrees/pt-manifest-run`（同基线HEAD）；与P001不共享写路径、数据库或生成输出。二者实现可并行，Go验证由控制器协调串行后分别集成。

P-006协议细化：合法重复padding占位不视为真实文件路径碰撞，限padding间同路径同长度；真实文件冲突仍拒绝。原始info哈希和占位对齐不改写；该例外已同步设计并告知解析worker，P002只映射实际可选文件。

集成基线前移至e7f6426（git记录作者hugh-zhan9，主题fix(build): initialize local signing during packaging）；已检查相对ebbbaa6的5文件delta仅签名脚本/说明与其测试，P001原文件逐字节相同。该外部提交已保留，候选仍归属原基线，控制器在新基线上应用完整P001补丁。P001与P006已集成但尚未解锁：P001待主验证/独立审查，P006两项Important修复中。

P001完成：完整12文件补丁已集成，主workspace `agent-go test ./services ./services/btclient -run ... -count=1 -timeout180s` exit0（services3.870s/btclient0.087s）；独立review_crop核对候选SHA且未发现Critical/Important。Keychain未读写用户实际钥匙串，外部站点未联调；此为连接/凭据/搜索切片，不代表完整下载链路。P006仍在修复协议/资源Important，P002暂不解锁。

P006完成：原始info身份、v1/v2/hybrid、padding与路径/资源预算已实现；主workspace test最终exit0（1.243s），独立复核确认3项协议/计算Important及目录前缀展开Important均修复，无新Critical/Important。P008原候选主回归13用例过，Minor过期/逐文件错误修复已集成待补测；P007最终模型接线修正验证中。

P007独立审阅修复中：扩大Configure维护代际保护，并按公开数据库身份隔离Keychain scope与本机OS锁，防跨manager/跨库GC误删。该外部Keychain一致性边界已先修订D-PT-04，不能只靠进程内mutex宣称多实例安全。

P003依赖细化：路径策略只依赖已验证P009模型，逐文件导入通过由消费方定义的远端资格verifier注入取得新鲜快照，不访问qB客户端，也不引用尚未完成的BT协调器；因此可与P007并行。P002/P004随后实现/装配该接口，不形成环依赖。P009是已完成模型子集的执行记录拆分，不是跳过验证。

P008完成：完整组件与两项Minor修复已集成，主workspace Vitest2文件16用例及design-system守卫通过；独立只读复核无Important。P001/P006/P008/P009完成，P007维护/凭据跨实例安全修复仍进行中，P003路径/导入实现进行中，P002/P004/P005尚未解锁。

P007完成：维护长lease/代际、动态数据库scope、跨进程文件锁和实际客户端PG端点身份均已修复；主workspace SQLite/PG TestBT最后exit0（3.968s/7.198s），独立复核Important全部闭合。P002解锁。P003正在最后维护/namespace验证，P004仍需等待P002/P003。

P003评审修复：恢复入口同样隔离，文件真实路径复核黑名单；原BTFile扩展publication_state用于崩溃后补发首次发布，具体语义已在D-PT-06先记载。P003独占此模型/迁移增量，P002只消费最终接口，集成后同步其工作区；回调沿用现有进程内语义，不宣称消息消费exactly-once。

P010提取前端交互编排以利用独立窗口：只写两个新文件并注入D-PT08 API，P004仍负责真实绑定与App/导航集成，所有后端依赖完成前不解锁P004。

P003首轮修复已按base/candidate SHA集成6文件；worker SQLite相关回归3.869s、迁移0.583s及vet通过，5个最小变异均被用例捕获。主集成验证尚待scanner软删两种恢复路径的第二增量，P003仍in_progress，不能据此解锁P004。P002真实隔离qB试验发现5.2.3 add响应为计数/added_torrent_ids JSON，适配修正中；尚无metadata-only能力通过证据。

P010主workspace已集成并定向Vitest通过26用例（实际命令证据见当前执行日志）；控制器复核了旧preview、未知分母和200条后历史可达修正。P006因真实qB hybrid拒绝证据重新打开，只修协议路径比较及对应fixture，保留既有安全/预算检查。P002的该部分等P006修正，其他已授权测试继续；不是把早先纯解析测试冒充客户端兼容性。

P002磁力能力实证已明确两组合差异，D-PT05/07先修为原子stopped=false+MetadataReceived，仅目标构建可用；原停止add再start被实验否定。该轮测试10.14s通过，但完整布局/构建profile及最终集成尚待验证，不能据此标记整个P002完成。

P006协议修正主区全包test exit0 0.394s；独立只读复核无Critical/Important/Minor，解析切片重新done，P002客户端布局仍单独验证。P003第二增量主SQLite相关检查exit0（services5.822s等），原一次性PG tmpfs空间耗尽后重建仅自有容器，重跑完整相关PG exit0（services93.630s/models0.921s/migrator1.646s，日志/tmp/cineinsight-pt-p003-pg-integrated.log）。复审发现旧失败分支未完整围栏task代际，第三增量修复中，P003不解锁。

P004进一步分成后端App装配与P011前端页面装配：写路径和生成输出由各自独占，P002/P003完成前都不派发；接口名/参数按D08与P010已定facade，最终绑定检查只在合并后P005执行，不以mock测试当真实接线通过。

P003完成：三轮修复已集成；最终主SQLite相关回归exit0 5.536s，聚焦PG导入/恢复/发布回归exit0 16.058s（第三修复前完整相关PG93.630s通过仍适用于未改范围），独立review_pt_spec复核此前Important已闭合，无新Critical/Important。第三增量旧逻辑red捕获file/Video/user-delete三种代际覆写，候选green/vet通过。尚未接App，因此P004/P011仍依赖P002完成。

P002原候选主SQLite4.580s、PG76.263s等通过，但独立审阅发现未决release、priority持久命令阶段、取消草稿重开、Drain超时恢复和大清单响应预算Important，故保持in_progress。P011实际只依赖已固定API/DTO与P010 facade：修正过宽依赖后可在独立frontend写集开展页面装配；P004仍等协调器通过再装配运行生命周期，最终P005必须实接口验证。

P002复审修复方案已先写D04/05/07：每外部写持久CAS claim、未决历史不可由空查询/后续成功清除、release严格守卫、取消草稿重开、Drain失败恢复、响应预算对齐。P002独占新增两模型列及迁移测试，P004尚未开始不发生共享写。P004恢复标记在真实DB替换前持久化、下一启动先冻结旧快照任务，消除备份后请求丢失；不扩大普通Resume。

P011已产22路径候选，相关442用例、受影响81/27用例及guard通过。因新btAPI导入尚未生成的App导出，暂保留为完整候选，待P004生成绑定后与后端顺序集成，不提前让主App导入缺失导出。P004需补当前TestBTConnection只返回status的D08缺口：非秘密版本与metadata能力检查并投影，前端显示版本/能力；不得用常量假造服务实际版本。

P002缓存崩溃边界修正已先写D05：task+digest不可变版本，先落新版本再CAS切DB指针，避免magnet→torrent覆盖后旧digest失配；仍是现有临时缓存、无新模型/事实源。启动必须以Submit持久SelectionDigest为确认凭据，磁力准备的重确认不能启动默认勾选文件。

恢复围栏补充覆盖SwitchBackendConfigOnly：源码现有流程已经打开并校验目标DB，可借其句柄取公开scope，在配置发布前由App注入hook持久标记；不改“只改配置”的目标库内容。普通全量迁移复制Drain后的最新意图，无需无差别污染所有任务。见D07，P004实现/测试该回收旧库场景。

P002 safety快照已主SQLite验证，状态复审关闭原4项及写前归属复核；客户端复审认可4MiB兼容修正但发现大wire后完整Unmarshal可能先分配数千万行，追加逐项解码前置限额修复。后续缓存/恢复/离线abandon等增量仍在开发，尚未解锁P004。

正常控制体验修正先记D07：后台观察使用ObservationEpoch并单独CAS，命令Revision不随进度tick改变，解决读状态后仅Poll就无法stop/release确认的问题；保持跨worker快照与P003 file eligibility围栏，初始化epoch高于既有资格避免ABA。P002拥有第三列与迁移回归。

P004的ValidateRoot须验证实际未来namespace/task保存范围，而非要求下载根的父目录自身是扫描根；复用BT导入的词法+真实路径/黑名单合同，保留path_not_in_library可读原因，不悄悄扩扫描根。必要路径策略接线列入writes，不在App重复实现库规则。

P003补一个明确的嵌套根边界：当前Withheld首个匹配即返回，released祖先可抢先放行新待下载子根/未知子namespace。按D06最具体归属及未知namespace优先补2文件回归，原已验证行为保持；此局部完成/复审前P004仍不解锁。

2026-10-31 P003嵌套/alias最终完成：两个坐标分别most-specific且各自资格检查，主相关SQLite2.979s、合并BT范围PG（services105.749s/models0.926s/migrator5.763s）通过，独立复审Important闭合无新增。P002 followup的正常观察与UI确认、无墙钟恢复、不可变缓存等主双库已通过；独立仍要求同task/DBscope缓存发布清理协调及旧失败观察epoch围栏，P002保持in_progress。客户端流式decode的新分配问题已关闭。

缓存清理复审方案已先记D05：公开DB scope/task隔离，已有OS锁机制覆盖发布+指针CAS+清理，固定DB lease且无SQL事务等待锁，GC不得跨scope；cache busy显式返回，测试注入scope仅作测试seam不作为生产回落。D07同步要求观察失败/结构迁移也带旧ObservationEpoch CAS，命令不绑观察版本。P002现有task*.go写域内实现，复用P007范围解析与锁工具不改其行为。

P004启动接线事实：main.go先NewApp再database.Init，markInterruptedSubtitleJobs已在wails.Run前处理本地恢复，不能仅依赖OnStartup（与UI并发）。BT路径策略构造期注入；scope/恢复标记/本地准入在schema后UI前完成。configureLocalMetadata只是观察者接线（禁用时取消已有backfill），可确保BT发布前已按设置接好，实际外部轮询/事件sink仍在startup配置后启动。

P004实施前定向设计复审发现两项Important并先修合同：配置暂停/维护暂停须独立持有，Resume不能反开仍运行的Configure（由P002补定点交错）；pg_restore普通失败不证明未提交，P004不能依赖RequiresRestart布尔清marker，须显式恢复开始阶段证据或保守保留并先消费BT围栏。无需新产品审批，保持普通服务既有失败恢复/qB持续运行。

P002完成：最终配置/维护暂停原因修复主TestBTTask4.484s通过，独立复审Important闭合；其余cacheguard主SQLite6.486s及受影响PG68.147s通过。最终12个有效mutation全部killed，race4.200s/vet通过，证据总表/tmp/cineinsight-pt-p002-evidence.json。P004解锁。根包相关App基线在未改接线前通过4.598s。P004隔离工作区已准备，并叠加待集成P011前端依赖；P011补丁四个new-file mode头及一处字符串外尾空白由controller仅格式规范化，源逻辑不变，采用/tmp/cineinsight-pt-p011-integratable.patch及其candidate清单。

P011独立复核仅余事件刷新风暴Important：bt-task-changed与5秒poll须共用单个in-flight刷新，最多一次pending补刷，覆盖分页/卸载；代际丢响应不能替代请求有界。P004已定TaskCenter kind=bt_download/action=open_bt_downloads仅导航，前端扩TaskCenterDrawer映射而不登记本地kill/cancel；summary selected_video_count优先服务端值。

P004/P011已顺序集成候选：P004服务20路径快照及6路径审阅修复、App/generated11路径、P011规范化28路径均核对base/candidate SHA后应用。服务四项Important修复有red→green记录，worker SQLite App3.690s/services7.348s/client0.321s/migrator0.501s、PG聚焦services12.374s/migrator1.342s通过；这些不是主集成最终验收。marker目录同步Important已复审闭合；App恢复顺序新Important（规则队列须先恢复再开放BT）及作者发现的BT runtime context同步仍在修正。P004/P011保持in_progress，最终全量与原位实际合成下载验收尚待P005。

P011完成：最终28路径候选与P004真实生成绑定均已集成。主区完整npm test脚本链（组件指定maxWorkers=1/无文件并行，后续脚本原样）exit0，206文件3131用例78.93s、全部静态守卫及498/498绑定双向消费通过；真实Vite build exit0。日志/tmp/cineinsight-pt-final-frontend.log与/tmp/cineinsight-pt-final-build.log。先前刷新风暴Important已独立复核闭合。P004仅余App恢复顺序/ctx同步增量与最终主Go验证，不能据前端通过宣称整体完成。

P004最后生产增量4路径（app_bt/runtimeContext同步、app_settings先恢复规则再BT、app_data_care无runtime也恢复本地队列、对应App测试）按精确SHA已集成；独立复审恢复顺序Important闭合，无新Critical/Important。主全量Go正在运行，最后仅替换测试侧顺序源字符串guard为确定性恢复中发布行为回归，不改生产合同。

P011补一个实际接线错误文案：P004的bt_restore_pending须提示下载管理恢复核实未完成，不能退回检查凭据。仅btAPI的既有btConnectionError映射及其测试增量，原3131/绑定/build证据适用于未改范围，补局部验证后关闭。

主全Go首轮exit1：App21.290s、database8.016s、migrator2.519s、models0.383s、btclient0.643s、btmanifest0.370s通过；services148.087s仅TestModelSchemaGolden缺新增三个BT表快照和TestMEDIA08EmbeddedSubtitleLiteralDefinedOnce扫描本轮.worktrees副本重复计数11失败。前者本轮遗漏，后者隔离验证目录引入的守卫范围问题；P004写域补充对应golden和测试排除目录，保持删除列检查及主源码唯一断言，修复后复跑受影响检查与services套件。

P004最后发现并实证SQLite最终库文件symlink在restore rename后scope改变，单旧scope marker可漏消费。D07先补双scope准备（当前物理+规范父目录/最终名）、票据逐项归属及部分准备失败规则，独立设计复核无C/I；不拒绝既有安装、不改BackupService恢复目标。该定点增量由P004实现，其他主验证继续。

P011恢复提示补充已主集成，btAPI与BTDownloadsPanel直接消费者均覆盖；定向2文件25测试exit0 728ms、真实build再次exit0，原完整3131及498接口守卫仍覆盖未改范围。一次误用npm组件脚本附加过滤的重复全量运行已主动中断exit130，不作为通过证据；正确直接Vitest定向命令的日志为/tmp/cineinsight-pt-restore-copy-focused.log。P004+guards主聚焦exit0（App2.578/services9.893/client0.328/models0.361/migrator0.522），仅SQLite symlink恢复身份增量未完成。

P004完成：最终双SQLite scope5路径按SHA集成，主App/restore/credential scope定向exit0（1.865s/1.028s），正常和旧单scope mutation证据有效（旧版真实restore后active被断言捕获exit1 0.831s），worker vet通过；独立复审最终双scope与整体后端组合边界无新C/I，之前Important全部闭合。先前root完整Go仅两个守卫失败均已修并主聚焦通过；P005最终services全套复核待P012真实链候选完成。P012进入隔离真实qB运行，当前重窗口归其worker。

P012实际链暴露P002正常add回读边界：两次真实运行在revision5/conflict/pending add/external_changed停止，20秒后真实任务归属/路径/清单均匹配、stoppedDL、progress0，尚未发priority写；不是通过mock假定流程成功。原P002重新打开定点诊断，先捕获第一次add后读状态以确认瞬态或其他真实原因，再按既定不盲重发写原则修复。P012测试保持严格，实际失败log/tmp/cineinsight-pt-p012-native-diagnostic.log；两轮自有qB进程/目录均清理。

P002首次add诊断已实证checkingResumeData/progress0/err=nil，既有归属/path/fullhash校验已通过，失败正是即时stopped断言。D05先记录最小正常就绪确认：仅原成功add/现有claim内5秒100ms可取消只读确认该状态，停止后原完整清单/priority验证，超时uncertain保留未决，取消sent冻结，绝不重发外部写；不新增阶段/字段或其它兼容状态。原owner实施并以P012实际链验证。

P012独立安全审阅发现复用nativehelper的清理注册早于端口/PID验证，动态端口抢占或复用时无条件HTTP shutdown可能影响别的客户端。扩大仅测试helper写域，改为只对本次command.Process发终止信号、等待/超时Kill，不调用未知归属WebUI关闭；修复前暂停新的native轮次，fake/只读验证继续。已有各轮日志自有PID清理通过，无观察到误关事实。

P012用add等待修复后已通过选择priority1/0/0，但暴露成功start响应后第一次仍停止：runControl唯一uncertain/Changed末尾分支、revision11/pending start，而稍后真实stalledDL。D05补同一目标状态合同的成功start/stop有界只读确认（仅确实发送写者、5秒100ms全身份/清单/priority核实、不重发命令），P012补明确停止/继续的实际验收。该次失败不作为完成证据，两个自有进程均清理。

P002 run-state等待2路径及P012最终native测试已按base/candidate SHA主集成；最终独立复审无新C/I，helper误关端点Important也已闭合。worker原样P012真实链exit0 10.610s：所选视频5088字节原位唯一入库/hash不变，priority1/0/0，Control stop=stoppedUP、start=stalledUP，localStop后仍stalledUP；自有PID29997/30023及profile清理断言通过。日志/tmp/cineinsight-pt-p002-native-run-wait.log。主同状态复跑及全量检查仍待完成，不据worker报告提前关闭P002/P012。

P002/P012最终主集成通过：CINEINSIGHT_BT_QB_PROBE=1 agent-go test ./services -run TestBTTask或精确SelectiveDownloadToLibrary -count=1 -timeout240s，exit0 41.175s，日志/tmp/cineinsight-pt-final-native.log。包含完整协调器定向回归和真实qB严格链/显式stop-start/原位hash/单条入库/外部做种及清理断言；与最终review SHA一致。两切片done，P005解锁，进入最终全Go、PG聚焦、文档事实核对和自有测试资源清理。

最终完成（2026-10-31）：完整主Go exit0（services174.720s）、主PG聚焦exit0（services13.463s/migrator9.337s）、主真实选择性下载与协调器联合41.175s、前端全量3131及最后25项增量/498绑定守卫/真实构建均通过。后端最终状态确认、恢复围栏、native清理和UI组合的独立审阅均无未解决Critical/Important。8文档已集成并核对本地引用，72个改动Go文件gofmt及手写diff检查通过；原用户frontend/package.json.md5 SHA保持不变。临时PG仅在确认自有label后停删及清理匿名卷，隔离qB进程/profile均通过清理断言。实现交付完成；真实Prowlarr/PT账号、系统Keychain及打包应用GUI仍是明确未验收环境，未安装部署、未提交或推送。完整证据见设计目录验证记录。
