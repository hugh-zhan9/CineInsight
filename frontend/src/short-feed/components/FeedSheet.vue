<template>
  <Teleport to="body">
  <div class="sheet-layer" :style="viewportStyle" @click.self="$emit('close')">
    <section class="sheet" :class="{ 'sheet--tall': tall }" role="dialog" :aria-label="label || title" @click.stop>
      <span class="sheet__grip" aria-hidden="true"></span>
      <header class="sheet__head">
        <div class="sheet__titles">
          <h2>{{ title }}</h2>
          <p v-if="subtitle" class="sheet__subtitle">{{ subtitle }}</p>
        </div>
        <p v-if="hint" class="sheet__hint">{{ hint }}</p>
        <slot name="head" />
        <!-- 显式关闭：点遮罩关闭依赖合成 click，浏览器里被舞台的 touch 拦截过一次就没了。
             标签面板按设计稿是右上角「完成」；评分与「更多」的关闭在底部按钮里（closeStyle none），其余是 ×。 -->
        <button v-if="closeStyle === 'icon'" type="button" class="btn-icon sheet__close" aria-label="关闭" data-test="sheet-close" @click="$emit('close')">
          <AppIcon name="close" :size="20" />
        </button>
        <button v-else-if="closeStyle === 'done'" type="button" class="btn-link sheet__done" data-test="sheet-close" @click="$emit('close')">完成</button>
      </header>
      <div class="sheet__scroll">
        <div ref="body" class="sheet__body" @scroll.passive="updateOverflow">
          <slot />
        </div>
        <!-- 深色面板里滚动条几乎看不见，最后一行又正好被按钮切掉：明确说一声下面还有。 -->
        <div v-if="hasMoreBelow" class="sheet__more" data-test="sheet-more" aria-hidden="true">还有更多，向上滑动查看</div>
      </div>
      <footer v-if="$slots.footer" class="sheet__footer">
        <slot name="footer" />
      </footer>
    </section>
  </div>
  </Teleport>
</template>

<script>
import AppIcon from '../../components/ui/AppIcon.vue';

// 手机端的底部面板：更多、评分、标签、播放范围共用同一个壳。
export default {
  name: 'FeedSheet',
  components: { AppIcon },
  props: {
    title: { type: String, required: true },
    // 标题下的一行（「更多」面板写类型与时长）。
    subtitle: { type: String, default: '' },
    hint: { type: String, default: '' },
    // 读屏名，缺省用标题（「更多」面板的标题是片名，读屏名是「更多操作」）。
    label: { type: String, default: '' },
    // 列表型面板（标签）几乎占满屏幕，少滚动。
    tall: { type: Boolean, default: false },
    // icon：右上角 ×；done：右上角「完成」；none：由底部按钮关闭。
    closeStyle: { type: String, default: 'icon', validator: value => ['icon', 'done', 'none'].includes(value) }
  },
  emits: ['close'],
  data() {
    return { viewportStyle: {}, hasMoreBelow: false };
  },
  mounted() {
    this.onKey = event => {
      if (event.key === 'Escape') this.$emit('close');
    };
    window.addEventListener('keydown', this.onKey);
    // iOS 的软键盘只缩小 visualViewport，布局视口和 vh 不一定变化。
    this.viewport = window.visualViewport;
    this.updateViewport();
    this.viewport?.addEventListener('resize', this.updateViewport);
    this.viewport?.addEventListener('scroll', this.updateViewport);
    this.$nextTick(this.updateOverflow);
  },
  updated() {
    // 插槽内容（标签列表）变了要重新判断是否溢出；值不变时 Vue 不会再触发更新。
    this.updateOverflow();
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.onKey);
    this.viewport?.removeEventListener('resize', this.updateViewport);
    this.viewport?.removeEventListener('scroll', this.updateViewport);
  },
  methods: {
    updateViewport() {
      if (!this.viewport) return;
      this.viewportStyle = {
        top: `${this.viewport.offsetTop}px`,
        left: `${this.viewport.offsetLeft}px`,
        width: `${this.viewport.width}px`,
        height: `${this.viewport.height}px`,
        bottom: 'auto',
        right: 'auto'
      };
      this.$nextTick(this.updateOverflow);
    },
    updateOverflow() {
      const body = this.$refs.body;
      if (!body) return;
      // 只有确实还有内容在可视区之下才提示；滚到底自动消失。
      this.hasMoreBelow = body.scrollHeight - body.clientHeight - body.scrollTop > 4;
    }
  }
};
</script>
