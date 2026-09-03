// 用 declarativeNetRequest 的会话规则给扩展自己发出的分片请求补上受限请求头。
//
// 为什么要这么绕：fetch() 不允许设置 Referer / Origin / User-Agent，浏览器会忽略掉。
// 而很多 CDN 恰恰只认这几个头，缺了就 403。DNR 是 MV3 里唯一能改这些头的路子。
//
// 作用域是这里最要紧的事，而且 MV3 给不出"只作用于本扩展的请求"这种条件，
// 所以要把实际边界说清楚，不能假装它是精确的：
//   1. tabIds: [-1] 匹配的是"不属于任何标签页的请求"。扩展自己的 fetch 是这种，
//      **但页面自己的 service worker / shared worker 发出的请求也是这种**；
//   2. urlFilter 收窄到这次任务真正要取的主机（有路径前缀时连路径一起收窄）；
//   3. resourceTypes 只留 xmlhttprequest。
//
// 因此残留的重叠是：任务进行期间，同一主机上由页面 service worker 发出的
// xmlhttprequest 也会被改写 Referer / Origin / User-Agent。这是 MV3 下没法再收窄的
// 部分，把窗口压到最小的办法就是规则只在任务运行期间存在——任务一结束立刻撤掉。
// 不要把这段注释改成"只作用于扩展自己的请求"，那不是事实。

import { DNR_RULE_ID_START, DNR_RULE_ID_END } from '../common/constants.js';

const TAB_ID_NONE = -1;

export class HeaderRuleManager {
  constructor(chromeApi) {
    this.chromeApi = chromeApi;
    this.rulesByTask = new Map(); // taskId -> ruleId[]
    this.nextRuleId = DNR_RULE_ID_START;
  }

  // 绕回区间开头时不能直接复用：还被在跑的任务占着的 ID 一旦重复，
  // updateSessionRules 会把整批 addRules 一起拒掉，那次任务就一个头都带不上。
  allocateRuleId() {
    const inUse = new Set();
    for (const ids of this.rulesByTask.values()) {
      for (const id of ids) inUse.add(id);
    }
    const span = DNR_RULE_ID_END - DNR_RULE_ID_START + 1;
    for (let step = 0; step < span; step += 1) {
      if (this.nextRuleId > DNR_RULE_ID_END) this.nextRuleId = DNR_RULE_ID_START;
      const candidate = this.nextRuleId++;
      if (!inUse.has(candidate)) return candidate;
    }
    throw new Error('请求头规则 ID 用尽了，说明有任务没有正常收尾');
  }

  // hosts 是这次任务会碰到的主机集合（播放列表、分片、密钥可能分散在不同域名上）。
  async apply(taskId, hosts, headers) {
    await this.clear(taskId);
    const requestHeaders = buildHeaderActions(headers);
    if (requestHeaders.length === 0) return;

    const uniqueHosts = [...new Set(hosts.filter(Boolean))];
    if (uniqueHosts.length === 0) return;

    const addRules = uniqueHosts.map((host) => ({
      id: this.allocateRuleId(),
      priority: 1,
      action: { type: 'modifyHeaders', requestHeaders },
      condition: {
        urlFilter: `||${host}^`,
        tabIds: [TAB_ID_NONE],
        resourceTypes: ['xmlhttprequest']
      }
    }));

    this.rulesByTask.set(taskId, addRules.map((rule) => rule.id));
    await this.chromeApi.declarativeNetRequest.updateSessionRules({ addRules });
  }

  // 先撤规则再销账。反过来的话，撤除失败就会留下一批没人认领的规则改到会话结束——
  // 那正是"任务已经结束却还在改用户请求头"的情形。
  async clear(taskId) {
    const ruleIds = this.rulesByTask.get(taskId);
    if (!ruleIds || ruleIds.length === 0) return;
    await this.chromeApi.declarativeNetRequest.updateSessionRules({ removeRuleIds: ruleIds });
    this.rulesByTask.delete(taskId);
  }

  // 进程重启后会话规则不一定还在，但残留的话就是"没有任务却在改请求头"。
  // 启动时把整个保留区间清一遍，宁可多删也不留下无主规则。
  async clearAll() {
    this.rulesByTask.clear();
    const existing = await this.chromeApi.declarativeNetRequest.getSessionRules();
    const removeRuleIds = existing
      .filter((rule) => rule.id >= DNR_RULE_ID_START && rule.id <= DNR_RULE_ID_END)
      .map((rule) => rule.id);
    if (removeRuleIds.length > 0) {
      await this.chromeApi.declarativeNetRequest.updateSessionRules({ removeRuleIds });
    }
  }
}

export function buildHeaderActions(headers) {
  const source = headers || {};
  const actions = [];
  if (source.referer) actions.push({ header: 'referer', operation: 'set', value: source.referer });
  if (source.origin) actions.push({ header: 'origin', operation: 'set', value: source.origin });
  if (source.userAgent) actions.push({ header: 'user-agent', operation: 'set', value: source.userAgent });
  return actions;
}

// 一条任务会碰到的主机：播放列表、分片、密钥。
export function hostsForTask({ playlistUrl, segmentUrls = [], keyUrls = [] }) {
  const hosts = new Set();
  for (const url of [playlistUrl, ...segmentUrls, ...keyUrls]) {
    if (!url) continue;
    try {
      hosts.add(new URL(url).host);
    } catch {
      // 解析不出主机的地址不配规则，取的时候自然会失败并报出来。
    }
  }
  return [...hosts];
}
