<template>
  <section class="page-content lists-page" data-test="lists-page">
    <PageHeader title="片单" :subtitle="activeTab.description">
      <template #below>
        <AppTabs :model-value="activeTab.key" :tabs="tabs" id-prefix="lists" aria-label="片单分类" @update:model-value="$emit('update:tab', $event)" />
      </template>
    </PageHeader>

    <!-- 想看、榜单、观影记录沿用原来各自页面的挂载方式：在这个标签上才挂载、离开即卸载。
         榜单一挂载就会报一次 OpenMovieChartYear（可能起后台抓取），不在这个标签上时不该有那个副作用；
         观影记录每次进来重读本地标记表，不用自己做失效。 -->
    <div v-if="pageActive && activeTab.key === 'watchlist'" id="lists-panel-watchlist" class="lists-page__panel" role="tabpanel" aria-labelledby="lists-tab-watchlist">
      <WatchlistPage embedded @find-resources="$emit('find-resources', $event)" @open-video-id="$emit('open-video-id', $event)" />
    </div>
    <div v-if="pageActive && activeTab.key === 'movie-chart'" id="lists-panel-movie-chart" class="lists-page__panel" role="tabpanel" aria-labelledby="lists-tab-movie-chart">
      <MovieChartPage embedded />
    </div>
    <div v-if="pageActive && activeTab.key === 'watched-movies'" id="lists-panel-watched-movies" class="lists-page__panel" role="tabpanel" aria-labelledby="lists-tab-watched-movies">
      <WatchedMoviesPage embedded @navigate="$emit('navigate', $event)" @open-video="$emit('open-video', $event)" />
    </div>
    <div v-if="resourcesMounted" v-show="activeTab.key === 'resources'" id="lists-panel-resources" class="lists-page__panel" role="tabpanel" aria-labelledby="lists-tab-resources">
      <BTDownloadsPanel :key="resourceRequestToken" :api="btAPI" :active="resourcesVisible" :initial-query="resourceQuery" @settings="$emit('settings', 'bt')" @play="$emit('open-video-id', $event)" />
    </div>
    <!-- 观看笔记首次进入才挂载，之后只隐藏：去原片核对再回来，编辑中的日记、年份与翻页都还在。 -->
    <div v-if="notesMounted" v-show="activeTab.key === 'viewing-notes'" id="lists-panel-viewing-notes" class="lists-page__panel" role="tabpanel" aria-labelledby="lists-tab-viewing-notes">
      <ViewingNotesPage
        embedded
        :page-active="pageActive && activeTab.key === 'viewing-notes'"
        @open-bookmark="$emit('open-bookmark', $event)"
        @open-video="$emit('open-video', $event)"
        @open-video-id="$emit('open-video-id', $event)"
      />
    </div>
  </section>
</template>

<script>
import BTDownloadsPanel from '../bt/BTDownloadsPanel.vue';
import { btAPI } from '../../utils/btAPI.js';
import MovieChartPage from '../MovieChartPage.vue';
import ViewingNotesPage from '../ViewingNotesPage.vue';
import WatchedMoviesPage from '../WatchedMoviesPage.vue';
import WatchlistPage from '../WatchlistPage.vue';
import AppTabs from '../ui/AppTabs.vue';
import PageHeader from '../ui/PageHeader.vue';
import { DEFAULT_LIST_TAB, LIST_TABS } from '../../utils/appCommands.js';

// 每个标签的说明，原来是各页页头的副标题（嵌在这里时各页不再显示自己的页头）。
const TAB_DESCRIPTIONS = {
  watchlist: '记下想看但还没收进片库的影片，选好类型后自动去对应的资料源补全信息；片库里已有的可以直接播放。',
  'movie-chart': '按年份浏览在内地公映的电影（含尚未上映的），看到合适的就直接标「想看 / 不想看 / 已看」。',
  'watched-movies': '你在年度榜单上标过「已看」的电影，按影片上映年份从新到旧分组。',
  resources: '浏览站点最新资源，或搜索片名；选择文件后交给本机 qBittorrent 下载。',
  'viewing-notes': '观影日记、片段书签，以及无法确认观看的播放记录。'
};

// 片单（D-P2-04，详细设计 §3.2）：一个页面五个标签，标签内容就是原来的四个页面（embedded，去掉各自的页头）。
// 当前标签是 App 的页面参数（prop tab），点标签发 update:tab，由 App 的 navigateTo('lists', { tab }) 落下；
// 本页不自己记标签。子页面的事件原样转给 App：navigate(key)（「去榜单」之类，别名由 App 换成标签）、
// open-video(video) / open-video-id(id) / open-bookmark(resolution)（App 按观看区进整页详情）。
export default {
  name: 'ListsPage',
  components: { BTDownloadsPanel, AppTabs, MovieChartPage, PageHeader, ViewingNotesPage, WatchedMoviesPage, WatchlistPage },
  props: {
    resourceQuery: { type: String, default: '' },
    resourceRequestToken: { type: Number, default: 0 },
    tab: { type: String, default: DEFAULT_LIST_TAB },
    pageActive: { type: Boolean, default: true }
  },
  emits: ['settings', 'find-resources', 'update:tab', 'navigate', 'open-video', 'open-video-id', 'open-bookmark'],
  data() {
    return { notesMounted: false, resourcesMounted: false, btAPI };
  },
  computed: {
    tabs() {
      return LIST_TABS.map(tab => ({ key: tab.key, label: tab.label, dataTest: `lists-tab-${tab.key}` }));
    },
    // 认不出的标签按默认标签（想看）显示。
    activeTab() {
      const tab = LIST_TABS.find(item => item.key === this.tab) || LIST_TABS.find(item => item.key === DEFAULT_LIST_TAB);
      return { ...tab, description: TAB_DESCRIPTIONS[tab.key] || '' };
    },
    resourcesVisible() { return this.pageActive && this.activeTab.key === 'resources'; },
    notesVisible() {
      return this.pageActive && this.activeTab.key === 'viewing-notes';
    }
  },
  watch: {
    resourcesVisible: { immediate: true, handler(visible) { if (visible) this.resourcesMounted = true; } },
    notesVisible: {
      immediate: true,
      handler(visible) {
        if (visible) this.notesMounted = true;
      }
    }
  }
};
</script>

<style scoped>
.lists-page { max-width: 1040px; }
.lists-page__panel { min-width: 0; }
</style>
