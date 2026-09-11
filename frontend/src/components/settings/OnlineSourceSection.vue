<template>
  <div id="settings-online-sources" class="settings-section">
    <h3>在线资料源</h3>
    <p class="help-text">
      想看片单靠这几个源补全标题、年份、简介与海报。每个源按条目类型分工：
      TMDB 管电影 / 电视剧 / 综艺，Bangumi 管动画，AV 先问 FANZA、查不到再兜底到 JavBus。
      凭证留空的源只会让对应类型补全不了，片单本身照常增删改查。
    </p>

    <div class="setting-item">
      <label>资料源出网代理</label>
      <input
        data-test="online-source-proxy-url"
        type="text"
        v-model.trim="form.metadata_proxy_url"
        placeholder="留空即直连；例如 socks5://127.0.0.1:1080 或 http://127.0.0.1:7890"
        class="text-input"
        autocomplete="off"
      />
      <!-- 本页另有一个「播放代理」分区，指的是本地转码缓存，跟出网没有半点关系。
           两者同名会造成真实误解，所以这一栏的名字必须带「资料源出网」四个字。 -->
      <p class="help-text">
        这四个源在国内大多直连不上。这里填的代理**只用于向资料源发请求**，与本页
        「播放代理」分区无关——那个说的是本地转码缓存。支持 http、https、socks5、socks5h；
        填错不会悄悄退回直连，而是直接报错。
      </p>
    </div>

    <div class="online-source">
      <h4>TMDB<span class="online-source__scope">电影 / 电视剧 / 综艺</span></h4>
      <div class="setting-item">
        <label>API Key</label>
        <input
          data-test="online-source-tmdb-api-key"
          type="password"
          v-model.trim="form.tmdb_api_key"
          placeholder="TMDB 账号设置里的 API Key (v3 auth)"
          class="text-input"
          autocomplete="off"
        />
      </div>
      <div class="online-source__test">
        <button
          data-test="online-source-test-tmdb"
          type="button"
          class="btn-secondary"
          :disabled="testing.tmdb"
          @click="testConnection('tmdb')"
        >{{ testing.tmdb ? '测试中…' : '测试连接' }}</button>
        <span
          v-if="results.tmdb"
          data-test="online-source-result-tmdb"
          :class="['online-source__result', results.tmdb.ok ? 'online-source__result--ok' : 'online-source__result--error']"
          role="status"
        >{{ resultText('tmdb') }}</span>
      </div>
    </div>

    <div class="online-source">
      <h4>Bangumi<span class="online-source__scope">动画</span></h4>
      <div class="setting-item">
        <label>Access Token</label>
        <input
          data-test="online-source-bangumi-access-token"
          type="password"
          v-model.trim="form.bangumi_access_token"
          placeholder="可留空；公开条目匿名可读"
          class="text-input"
          autocomplete="off"
        />
        <p class="help-text">Bangumi 的公开条目不需要凭证也能查，填了 Token 只是提高配额上限。</p>
      </div>
      <div class="online-source__test">
        <button
          data-test="online-source-test-bangumi"
          type="button"
          class="btn-secondary"
          :disabled="testing.bangumi"
          @click="testConnection('bangumi')"
        >{{ testing.bangumi ? '测试中…' : '测试连接' }}</button>
        <span
          v-if="results.bangumi"
          data-test="online-source-result-bangumi"
          :class="['online-source__result', results.bangumi.ok ? 'online-source__result--ok' : 'online-source__result--error']"
          role="status"
        >{{ resultText('bangumi') }}</span>
      </div>
    </div>

    <div class="online-source">
      <h4>FANZA<span class="online-source__scope">AV</span></h4>
      <div class="setting-item">
        <label>API ID</label>
        <input
          data-test="online-source-fanza-api-id"
          type="password"
          v-model.trim="form.fanza_api_id"
          placeholder="DMM 联盟后台的 api_id"
          class="text-input"
          autocomplete="off"
        />
      </div>
      <div class="setting-item">
        <label>Affiliate ID</label>
        <input
          data-test="online-source-fanza-affiliate-id"
          type="text"
          v-model.trim="form.fanza_affiliate_id"
          placeholder="DMM 联盟后台的 affiliate_id，形如 xxxxx-990"
          class="text-input"
          autocomplete="off"
        />
        <p class="help-text">两项都是 FANZA 接口的必传参数，缺一个就不会发请求。</p>
      </div>
      <div class="online-source__test">
        <button
          data-test="online-source-test-fanza"
          type="button"
          class="btn-secondary"
          :disabled="testing.fanza"
          @click="testConnection('fanza')"
        >{{ testing.fanza ? '测试中…' : '测试连接' }}</button>
        <span
          v-if="results.fanza"
          data-test="online-source-result-fanza"
          :class="['online-source__result', results.fanza.ok ? 'online-source__result--ok' : 'online-source__result--error']"
          role="status"
        >{{ resultText('fanza') }}</span>
      </div>
    </div>

    <div class="online-source">
      <h4>JavBus<span class="online-source__scope">AV 兜底</span></h4>
      <p class="help-text">不需要凭证，只在 FANZA 明确查不到时才问它。它抓的是网页，所以更容易被反爬挡住——测一下就知道现在通不通。</p>
      <div class="online-source__test">
        <button
          data-test="online-source-test-javbus"
          type="button"
          class="btn-secondary"
          :disabled="testing.javbus"
          @click="testConnection('javbus')"
        >{{ testing.javbus ? '测试中…' : '测试连接' }}</button>
        <span
          v-if="results.javbus"
          data-test="online-source-result-javbus"
          :class="['online-source__result', results.javbus.ok ? 'online-source__result--ok' : 'online-source__result--error']"
          role="status"
        >{{ resultText('javbus') }}</span>
      </div>
    </div>

    <p class="help-text">测试连接用的是上面**当前填写的值**，不必先保存；留空的字段按已保存或环境变量配置。</p>
  </div>
</template>

<script>
import { TestWatchlistMetadataConnection } from '../../../wailsjs/go/main/App';

// 在线资料源分区：出网代理、三家凭证，以及每个源一个连接探测按钮。
//
// 探测不在这里判断成败原因——凭证缺失 / 凭证无效 / 代理不通 / 网络不可达是后端
// 在真正出网那一刻认定的（D-WM14），这里只负责把它给的那句话显示出来。前端自己
// 猜一套原因，只会和后端的判定对不上。
export default {
  name: 'OnlineSourceSection',
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      testing: { tmdb: false, bangumi: false, fanza: false, javbus: false },
      results: { tmdb: null, bangumi: null, fanza: null, javbus: null }
    };
  },
  methods: {
    // 用表单当前值测，不要求先保存——照 AI 标签分区「测试连接」的既有做法。
    async testConnection(source) {
      if (this.testing[source]) return;
      this.testing[source] = true;
      this.results[source] = null;
      try {
        this.results[source] = await TestWatchlistMetadataConnection(this.probePayload(source));
      } catch (err) {
        this.results[source] = { ok: false, message: '测试失败：' + (err instanceof Error ? err.message : String(err)) };
      } finally {
        this.testing[source] = false;
      }
    },
    // 只带这个源用得到的凭证。探测 Bangumi 没有理由把 TMDB 的 Key 一起递过去——
    // 少一条经过的路，就少一处可能泄漏的地方。没带的字段后端按已保存值回落。
    probePayload(source) {
      const payload = { source, proxy_url: this.form.metadata_proxy_url || '' };
      if (source === 'tmdb') payload.tmdb_api_key = this.form.tmdb_api_key || '';
      if (source === 'bangumi') payload.bangumi_access_token = this.form.bangumi_access_token || '';
      if (source === 'fanza') {
        payload.fanza_api_id = this.form.fanza_api_id || '';
        payload.fanza_affiliate_id = this.form.fanza_affiliate_id || '';
      }
      return payload;
    },
    // 结果里带上这次到底走没走代理：同一句"网络不可达"，直连和走代理的下一步完全不同。
    // proxy_url 是后端脱敏过的形态（只剩协议和主机），不含口令。
    //
    // 调用本身就没成功时（后端没答话）这个字段压根不存在，那时不能说"直连"——
    // 我们并不知道这次走的哪条路，编一个只会把人引偏。空串才是后端说的"直连"。
    resultText(source) {
      const result = this.results[source];
      if (!result) return '';
      if (result.proxy_url === undefined || result.proxy_url === null) return result.message;
      return `${result.message}${result.proxy_url ? `（经代理 ${result.proxy_url}）` : '（直连）'}`;
    }
  }
};
</script>

<style scoped>
.online-source {
  margin-top: 16px;
  padding-top: 12px;
  border-top: 1px solid var(--border-color);
}

.online-source h4 {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px;
  margin: 0 0 8px;
}

.online-source__scope {
  font-size: 12px;
  font-weight: normal;
  color: var(--text-secondary);
}

.online-source__test {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}

.online-source__result {
  min-width: 0;
  font-size: 13px;
  overflow-wrap: anywhere;
}

.online-source__result--ok { color: var(--success-color); }
.online-source__result--error { color: var(--danger-color); }
</style>
