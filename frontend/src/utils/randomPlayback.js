// 片库随机播放的本机偏好（D-PC44、PLAY-08）：随机条件与「最近 12 次」排除表存在 localStorage，
// 重启后仍然生效。只存条件与 ID 数组，读写都包 try/catch——隐私模式或存储被禁用时照常可用，只是不记忆。
//
// 2026-10-11 随机播放与「今晚看什么」合并：两者共用一套随机条件（观看状态、仅收藏、整片时长上限），
// 存在 library-random-conditions；旧的 library-random-mode（均衡 / 仅未看 / 仅收藏）在新键缺失时迁移读入。
//
// UI 二期（D-P2-01）：「随机播放一部」的流程（主键、今晚看什么里的「随机播放一部」、结果条「换一个」）从片库页
// 抽到这里的 randomPlaybackMixin，片库管理页（VideoListPage）与视频海报墙（viewer/VideoBrowsePage）共用。
import { PlayRandomVideoWithFilter, RerollRandom } from '../../wailsjs/go/main/App';
import { notify, notifyError } from '../utils/feedback.js';

export const RANDOM_CONDITIONS_STORAGE_KEY = 'library-random-conditions';
export const LEGACY_RANDOM_MODE_STORAGE_KEY = 'library-random-mode';
export const RANDOM_RECENT_STORAGE_KEY = 'library-random-recent';
export const RANDOM_RECENT_LIMIT = 12;
// 随机播放启动后允许「换一个」（不计入统计）的时长，与后端 randomRerollWindow 一致。
export const RANDOM_REROLL_WINDOW_MS = 30 * 1000;
export const RANDOM_WATCH_STATES = ['unwatched', 'any', 'watched'];
export const RANDOM_WATCH_LABELS = { unwatched: '探索', any: '不限', watched: '重温' };
export const RANDOM_MAX_MINUTES = 1440;
export const DEFAULT_RANDOM_CONDITIONS = Object.freeze({ watchState: 'any', favoritesOnly: false, maxMinutes: 0 });

function localStore() {
  try {
    return window.localStorage || null;
  } catch (_err) {
    return null;
  }
}

export function validRandomMinutes(value) {
  return Number.isInteger(value) && value >= 0 && value <= RANDOM_MAX_MINUTES;
}

// 不认识的取值逐项回落到默认，不让一处坏值连带清掉其余条件。
export function normalizeRandomConditions(value) {
  const source = value && typeof value === 'object' ? value : {};
  return {
    watchState: RANDOM_WATCH_STATES.includes(source.watchState) ? source.watchState : DEFAULT_RANDOM_CONDITIONS.watchState,
    favoritesOnly: source.favoritesOnly === true,
    maxMinutes: validRandomMinutes(source.maxMinutes) ? source.maxMinutes : DEFAULT_RANDOM_CONDITIONS.maxMinutes
  };
}

function conditionsFromLegacyMode(mode) {
  if (mode === 'unwatched') return { ...DEFAULT_RANDOM_CONDITIONS, watchState: 'unwatched' };
  if (mode === 'favorites') return { ...DEFAULT_RANDOM_CONDITIONS, favoritesOnly: true };
  return { ...DEFAULT_RANDOM_CONDITIONS };
}

export function loadRandomConditions() {
  try {
    const store = localStore();
    const raw = store?.getItem(RANDOM_CONDITIONS_STORAGE_KEY);
    if (raw) return normalizeRandomConditions(JSON.parse(raw));
    return conditionsFromLegacyMode(store?.getItem(LEGACY_RANDOM_MODE_STORAGE_KEY));
  } catch (_err) {
    return { ...DEFAULT_RANDOM_CONDITIONS };
  }
}

export function saveRandomConditions(conditions) {
  try {
    localStore()?.setItem(RANDOM_CONDITIONS_STORAGE_KEY, JSON.stringify(normalizeRandomConditions(conditions)));
  } catch (_err) {}
}

// 发给后端 RandomPlayRequest 的条件字段（随机播放、随机 N 部、今晚看什么共用）。
export function randomConditionFields(conditions) {
  const normalized = normalizeRandomConditions(conditions);
  return {
    watch_state: normalized.watchState,
    favorites_only: normalized.favoritesOnly,
    max_duration_seconds: normalized.maxMinutes * 60
  };
}

// 生效条件的短标签，用在随机按钮上；不限、不限收藏、不限时长都不出现。
export function randomConditionTags(conditions) {
  const normalized = normalizeRandomConditions(conditions);
  const tags = [];
  if (normalized.watchState !== 'any') tags.push(RANDOM_WATCH_LABELS[normalized.watchState]);
  if (normalized.favoritesOnly) tags.push('收藏');
  if (normalized.maxMinutes > 0) tags.push(`≤${normalized.maxMinutes}分钟`);
  return tags;
}

// 去重、去掉非法值，保留最近的 12 个（后出现的算更近）。
export function normalizeRecentRandomIDs(ids) {
  const result = [];
  for (const value of Array.isArray(ids) ? ids : []) {
    const id = Number(value);
    if (!Number.isInteger(id) || id <= 0) continue;
    const existing = result.indexOf(id);
    if (existing !== -1) result.splice(existing, 1);
    result.push(id);
  }
  return result.slice(-RANDOM_RECENT_LIMIT);
}

export function loadRecentRandomIDs() {
  try {
    const raw = localStore()?.getItem(RANDOM_RECENT_STORAGE_KEY);
    if (!raw) return [];
    return normalizeRecentRandomIDs(JSON.parse(raw));
  } catch (_err) {
    return [];
  }
}

export function saveRecentRandomIDs(ids) {
  try {
    localStore()?.setItem(RANDOM_RECENT_STORAGE_KEY, JSON.stringify(normalizeRecentRandomIDs(ids)));
  } catch (_err) {}
}

export function rerollWindowOpen(startedAt, now = Date.now()) {
  const started = Number(startedAt) || 0;
  return started > 0 && now - started < RANDOM_REROLL_WINDOW_MS;
}

// 两个页面各自持有一份条件与排除表（data），写进同一份 localStorage；页面切回来（pageActive 变 true）时从存储里
// 读回，另一个页面改过的条件与新随机到的片在这里也生效。存储不可用（读即抛错）时返回 null，保留页面内存里那份。
export function readStoredRandomState() {
  try {
    if (!window.localStorage) return null;
  } catch (_err) {
    return null;
  }
  return { conditions: loadRandomConditions(), recentIDs: loadRecentRandomIDs() };
}

// 发给 PlayRandomVideoWithFilter 的请求：当前片库筛选 + 随机条件 + 最近 12 次随机结果作排除。
export function randomPlayRequest(filter, conditions, recentIDs) {
  return {
    filter,
    ...randomConditionFields(conditions),
    exclude_ids: normalizeRecentRandomIDs(recentIDs)
  };
}

// 随机拆分按钮 ▾ 里的条件开关（与片库工具栏同一组）：观看状态（探索 / 不限 / 重温）是硬过滤，仅收藏也是；
// 时长上限只在今晚看什么里设，这里只能取消。宿主在前面加自己的入口（今晚看什么…、随机 N 部）。
export function randomConditionMenuItems(conditions) {
  const normalized = normalizeRandomConditions(conditions);
  const items = [
    { heading: '观看状态' },
    { id: 'watch:unwatched', label: '探索 · 只抽没看过的', checked: normalized.watchState === 'unwatched' },
    { id: 'watch:any', label: '不限', checked: normalized.watchState === 'any' },
    { id: 'watch:watched', label: '重温 · 只抽看过的', checked: normalized.watchState === 'watched' },
    { divider: true },
    { id: 'favorites', label: '仅收藏', checked: normalized.favoritesOnly }
  ];
  if (normalized.maxMinutes > 0) {
    items.push({ id: 'duration:clear', label: `取消时长上限（≤${normalized.maxMinutes} 分钟）` });
  }
  return items;
}

// 菜单里一项条件开关对应的新条件；不是条件开关（今晚看什么、随机 N 部…）时返回 null。
export function applyRandomConditionItem(conditions, itemID) {
  const id = String(itemID || '');
  const normalized = normalizeRandomConditions(conditions);
  if (id.startsWith('watch:')) {
    const watchState = id.slice(6);
    return RANDOM_WATCH_STATES.includes(watchState) ? { ...normalized, watchState } : null;
  }
  if (id === 'favorites') return { ...normalized, favoritesOnly: !normalized.favoritesOnly };
  if (id === 'duration:clear') return { ...normalized, maxMinutes: 0 };
  return null;
}

export const EMPTY_RANDOM_PLAY = Object.freeze({ active: false, video: null, reason: '', token: '', startedAt: 0, rerollable: false, loading: false });

// 「随机播放一部」（D-PC43/44、PLAY-08）。宿主提供：
//   randomPlayFilter()：当前的 LibraryFilter（随机只在这个范围里抽）；
//   randomPlayBlockedReason()（可选）：不能随机时的说明，返回非空串就只提示不请求（片库页的语义搜索模式）；
//   applyPlaybackAttemptResult(result)：PlaybackAttemptResult 落到宿主的列表（行状态、失效、失败提示）；
//   pageActive（可选 prop）：变回 true 时从存储读回条件与排除表。
// 结果条的数据是 randomPlayBanner（交给 video-list/RandomPickBanner 的 randomPlay），事件接 rerollRandomPlay / dismissRandomPlay。
export const randomPlaybackMixin = {
  data() {
    return {
      randomConditions: loadRandomConditions(),
      recentRandomVideoIDs: loadRecentRandomIDs(),
      randomPlay: { ...EMPTY_RANDOM_PLAY }
    };
  },
  computed: {
    randomPlayBanner() {
      const play = this.randomPlay;
      if (!play.active || !play.video) return null;
      return {
        active: true,
        videoName: play.video.display_title || play.video.name || `视频 #${play.video.id}`,
        reason: play.reason,
        rerollable: play.rerollable,
        loading: play.loading
      };
    }
  },
  watch: {
    randomConditions(conditions) {
      saveRandomConditions(conditions);
    },
    pageActive(active) {
      if (active) this.syncRandomStateFromStorage();
    }
  },
  beforeUnmount() {
    clearTimeout(this._randomRerollTimer);
  },
  methods: {
    syncRandomStateFromStorage() {
      const stored = readStoredRandomState();
      if (!stored) return;
      if (JSON.stringify(stored.conditions) !== JSON.stringify(normalizeRandomConditions(this.randomConditions))) {
        this.randomConditions = stored.conditions;
      }
      if (JSON.stringify(stored.recentIDs) !== JSON.stringify(this.recentRandomVideoIDs)) {
        this.recentRandomVideoIDs = stored.recentIDs;
      }
    },
    // ▾ 菜单里的条件开关：改了就回写（watch 存进 localStorage）；不是条件开关返回 false，宿主自己处理。
    applyRandomConditionItem(itemID) {
      const next = applyRandomConditionItem(this.randomConditions, itemID);
      if (!next) return false;
      this.randomConditions = next;
      return true;
    },
    async playRandom() {
      const blocked = typeof this.randomPlayBlockedReason === 'function' ? this.randomPlayBlockedReason() : '';
      if (blocked) {
        notify(blocked);
        return;
      }
      if (this.randomPlay.loading) return;
      this.randomPlay = { ...this.randomPlay, loading: true };
      try {
        const result = await PlayRandomVideoWithFilter(randomPlayRequest(this.randomPlayFilter(), this.randomConditions, this.recentRandomVideoIDs));
        await this.handleRandomPlayResult(result);
      } catch (err) {
        console.error('随机播放失败:', err);
        notifyError('随机播放失败: ' + err);
      } finally {
        this.randomPlay = { ...this.randomPlay, loading: false };
      }
    },
    // 结果条上的「换一个」（D-PC44）：30 秒窗口内用令牌换，这次随机不计数、不记账；
    // 窗口已过、没有令牌，或后端答 reroll_expired（已提交）时，改为普通的再随机一次。
    async rerollRandomPlay() {
      const current = this.randomPlay;
      if (current.loading) return;
      if (!current.token || !rerollWindowOpen(current.startedAt)) {
        await this.playRandom();
        return;
      }
      this.randomPlay = { ...current, loading: true };
      let expired = false;
      try {
        const result = await RerollRandom(current.token);
        if (result?.reason_code === 'reroll_expired') {
          expired = true;
        } else {
          await this.handleRandomPlayResult(result);
        }
      } catch (err) {
        console.error('换一个失败:', err);
        notifyError('换一个失败: ' + err);
      } finally {
        this.randomPlay = { ...this.randomPlay, loading: false };
      }
      if (expired) {
        this.randomPlay = { ...this.randomPlay, token: '', rerollable: false };
        await this.playRandom();
      }
    },
    async handleRandomPlayResult(result) {
      if (result?.dispatch_succeeded && result.video) {
        this.rememberRandomVideo(result.video.id);
        this.showRandomPlayResult(result);
        // 返回的 video 是库里的现值：计数与 last_played_at 要等 30 秒后提交才变，这里不自行加一。
        await this.applyPlaybackAttemptResult(result);
        return;
      }
      // 没抽到或没播起来：结果条上那一部已经不是「正在随机播放」的了。
      this.dismissRandomPlay();
      await this.applyPlaybackAttemptResult(result);
    },
    showRandomPlayResult(result) {
      clearTimeout(this._randomRerollTimer);
      const token = String(result.reroll_token || '');
      this.randomPlay = {
        active: true,
        video: result.video,
        reason: result.selection_reason || '按当前筛选条件选择',
        token,
        startedAt: Date.now(),
        rerollable: Boolean(token),
        loading: this.randomPlay.loading
      };
      if (!token) return;
      // 窗口到点就把结果条改成「已计入」；令牌本身由后端判定，这里只管说法。
      this._randomRerollTimer = setTimeout(() => {
        if (this.randomPlay.token === token) this.randomPlay = { ...this.randomPlay, rerollable: false };
      }, RANDOM_REROLL_WINDOW_MS);
    },
    dismissRandomPlay() {
      clearTimeout(this._randomRerollTimer);
      this.randomPlay = { ...EMPTY_RANDOM_PLAY, loading: this.randomPlay.loading };
    },
    rememberRandomVideo(videoID) {
      this.recentRandomVideoIDs = normalizeRecentRandomIDs([...this.recentRandomVideoIDs, videoID]);
      saveRecentRandomIDs(this.recentRandomVideoIDs);
    }
  }
};
