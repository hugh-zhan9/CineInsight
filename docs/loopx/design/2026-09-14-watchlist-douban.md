# 想看电影接入豆瓣（2026-09-14）

用户裁决：电影优先豆瓣，未找到时 TMDB 兜底。只改变想看片单的 movie 类型；不迁移已成功或手动维护的记录。已有失败条目由用户重试。

## 数据与路由

- 公开网页搜索：`https://movie.douban.com/j/subject_suggest?q=...`。映射 id/title/sub_title/year/img；按源顺序返回，排除带 episode 的剧集。
- 移动网页详情：`https://m.douban.com/rexxar/api/v2/movie/<id>`。映射 title/original_title/year/intro/rating.value/genres/directors/actors/pic；再次核对电影类型及 ID。无需 Cookie 或 API Key。
- 共用现有客户端工厂与资料源代理；不改为隐式直连。响应最多 2 MiB。日志只记路由、状态、字节数和耗时。
- 搜索 `[]`、详情 404 且 JSON 的 `code=404,msg=traversal_error` 是未找到；验证重定向、HTML、null、结构损坏、未知 HTTP 404、403/429 都归源异常。沿用只有未找到才继续的链策略。
- 自动补全在同一源上搜索并取第一候选详情；重选候选携带 `douban:<id>` / `tmdb:<id>` 的选择标识，定向取详情，落库仍是 source_name 加原始 source_item_id。避免跨源数字 ID 碰撞。多源电影不接受缺来源的旧候选，提示刷新。
- 豆瓣测试连接同时查搜索和详情，避免只测搜索时掩盖详情验证拦截。电影凭证失败仍提示 TMDB（豆瓣不需要凭证）。

## 验证

- Go 想看片单及资料源测试通过：`go test ./services -run 'Test(Watchlist|ProbeWatchlistMetadata)' -count=1 -timeout 180s`。覆盖字段映射、电影/剧集区分、异常分类、兜底条件、候选带来源往返落库。
- Vue 相关测试 41 项通过：`npx vitest run src/components/WatchlistPage.test.js src/components/settings/OnlineSourceSection.test.js`。
- `npm run build`、`go build` 通过。前端保留既有大 chunk / 动态导入警告。
- 真实 Go 适配器直连：沙丘（3001114，2021，评分 7.7）、肖申克的救赎（1292052，1994，评分 9.7）均取得中文简介、类型、导演、演员。评分为当次响应，不做常量使用。
- 调用实际 DownloadPoster 保存到临时托管目录：肖申克海报成功；沙丘海报返回 HTTP 200、Content-Type text/html 的验证页，图片格式校验拒绝。改 Referer 为电影站/移动站也未解决，不冒充有效图片。按既有补全语义，海报失败不阻断文字资料。
- 应用已配置代理下，搜索与肖申克详情取得 200；部分请求超时。上述网页接口和图床可能限流或要求验证，未承诺持续可用。
- 未改生产数据库、未覆盖已安装应用。现有成功条目不会自动重跑。
