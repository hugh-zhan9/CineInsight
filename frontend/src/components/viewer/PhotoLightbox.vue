<template>
  <!-- 挂到 body 末尾：盖住顶栏，也排在之后才打开的确认框、提示之下（同在 --z-modal 时按 DOM 先后叠放）。 -->
  <Teleport to="body">
    <DialogShell
      ref="shell"
      class="photo-lightbox"
      size="full"
      :title="image.name || '图片'"
      close-data-test="photo-lightbox-close"
      data-test="photo-lightbox"
      @close="$emit('close')"
    >
      <div class="photo-lightbox__layout">
        <div class="photo-lightbox__stage">
          <img
            v-if="!imageFailed"
            :key="image.id"
            :src="originalURL"
            :alt="image.name"
            class="photo-lightbox__img"
            data-test="photo-lightbox-img"
            @error="imageFailed = true"
          />
          <div v-else class="photo-lightbox__fallback" role="status" data-test="photo-lightbox-fallback">
            <AppIcon name="image" :size="24" />
            <p>这张图片打不开：文件可能已移动、所在的磁盘没有连接，或这种格式（如 HEIC、RAW）在当前平台无法显示。</p>
          </div>
          <button
            type="button"
            class="btn-icon photo-lightbox__nav photo-lightbox__nav--prev"
            aria-label="上一张"
            title="上一张（←）"
            :disabled="!hasPrev"
            data-test="photo-lightbox-prev"
            @click="$emit('prev')"
          ><AppIcon name="chevron-left" :size="24" /></button>
          <button
            type="button"
            class="btn-icon photo-lightbox__nav photo-lightbox__nav--next"
            :aria-label="loadingNext ? '正在加载下一批' : '下一张'"
            title="下一张（→）"
            :disabled="!hasNext || loadingNext"
            data-test="photo-lightbox-next"
            @click="$emit('next')"
          ><AppSpinner v-if="loadingNext" :size="20" /><AppIcon v-else name="chevron-right" :size="24" /></button>
          <AppBanner v-if="nextError" class="photo-lightbox__next-error" tone="danger" title="下一批没有加载出来" :message="nextError" data-test="photo-lightbox-next-error">
            <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="photo-lightbox-next-retry" @click="$emit('next')">重试</button></template>
          </AppBanner>
        </div>

        <aside class="photo-lightbox__info" aria-label="图片信息" data-test="photo-lightbox-info">
          <div class="photo-lightbox__actions">
            <button
              type="button"
              :class="['btn', 'btn--secondary', 'photo-lightbox__favorite', { 'photo-lightbox__favorite--on': image.is_favorite }]"
              :aria-pressed="image.is_favorite ? 'true' : 'false'"
              title="收藏（F）"
              :disabled="favoriteBusy"
              data-test="photo-lightbox-favorite"
              @click="toggleFavorite"
            ><AppIcon name="star" :size="16" />{{ image.is_favorite ? '已收藏' : '收藏' }}</button>
            <RatingStars :value="rating" :disabled="ratingBusy" @rate="saveRating" />
          </div>
          <AppBanner v-if="actionError" tone="danger" :title="actionError" data-test="photo-lightbox-error" />

          <dl class="photo-lightbox__facts" data-test="photo-lightbox-facts">
            <template v-for="fact in facts" :key="fact.key">
              <dt>{{ fact.label }}</dt><dd :title="fact.title || fact.value">{{ fact.value }}</dd>
            </template>
          </dl>

          <section class="photo-lightbox__block" data-test="photo-lightbox-people">
            <h3>人物</h3>
            <p v-if="detail.loading" class="photo-lightbox__muted"><AppSpinner :size="16" /> 正在读取…</p>
            <p v-else-if="detail.error" class="photo-lightbox__muted" role="status">{{ detail.error }}</p>
            <p v-else-if="!detail.people.length" class="photo-lightbox__muted">这张图片还没有标出人物</p>
            <div v-else class="photo-lightbox__people">
              <TagPill
                v-for="item in detail.people"
                :key="item.person.id"
                :label="item.person.display_name"
                clickable
                :title="`去看${item.person.display_name}`"
                :data-test="`photo-lightbox-person-${item.person.id}`"
                @click="$emit('open-person', { id: Number(item.person.id), name: item.person.display_name })"
              ><template #leading><AppIcon name="person" :size="16" /></template></TagPill>
            </div>
          </section>

          <section class="photo-lightbox__block">
            <WallpaperButton kind="image" :media-id="Number(image.id)" />
            <button type="button" class="btn btn--secondary btn--sm photo-lightbox__manage" data-test="photo-lightbox-open-manage" @click="$emit('open-in-manage', image)">在图片管理中打开</button>
          </section>
        </aside>
      </div>
    </DialogShell>
  </Teleport>
</template>

<script>
// 观看侧的全屏看图（D-P2-08，详细设计 §3.11）：照片流点一张进来。只看、收藏、评分、设为壁纸，
// 没有删除、打标签、AI、批量、目录操作；要整理这一张就「在图片管理中打开」。
// - 骨架是 DialogShell（size=full），在 BaseModal 的打开栈里：Esc 只关最上面一层，叠在上面的确认框先收到 Esc；
//   ← → 与 F（收藏）也只在本层是最上面一层、焦点不在输入框里时才接。
// - 列表与当前是哪一张归宿主（PhotoBrowsePage）：本组件只发 prev / next（到已加载末尾时由宿主续载再前进）、
//   patch（改本页那一张：收藏、评分先改后存，失败改回原值；详情与查看次数读到后并进去）、changed（存成功的改动，宿主转给图片管理）。
// - 显示满 VIEW_RECORD_DELAY_MS 才记一次查看（与图片管理同口径），原图打不开不算；失败只记日志。
// 事件：close、prev、next、patch(fields)、changed(fields)、open-person({ id, name })、open-in-manage(image)。
import { GetImageDetail, RecordImageView, SetImageFavorite, SetImageRating } from '../../../wailsjs/go/main/App';
import { VIEW_RECORD_DELAY_MS } from '../PhotoLibraryPage.vue';
import WallpaperButton from '../WallpaperButton.vue';
import AppBanner from '../ui/AppBanner.vue';
import AppIcon from '../ui/AppIcon.vue';
import AppSpinner from '../ui/AppSpinner.vue';
import DialogShell from '../ui/DialogShell.vue';
import TagPill from '../ui/TagPill.vue';
import RatingStars from './RatingStars.vue';
import { translateBackendError } from '../../utils/feedback.js';
import { logFrontend } from '../../utils/frontendLog.js';
import { isTextEntryTarget } from '../../utils/keyboardShortcuts.js';

export function photoOriginalURL(id) {
  return `/preview/image/${Number(id)}`;
}

// 所在文件夹只写最后一级名字，完整路径放在悬停提示里。
export function photoFolderName(directory) {
  const path = String(directory || '').replace(/[\\/]+$/, '');
  return path.split(/[\\/]/).pop() || path;
}

function formatTakenAt(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString('zh-CN', { year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

// 信息面板（详细设计 §3.11）：拍摄时间、相机与参数、尺寸、文件夹。没有值的项不列；拍摄时间缺失时写明没有记录
// （照片流此时按入库时间排它）。
export function photoLightboxFacts(image) {
  if (!image) return [];
  const camera = [image.camera_make, image.camera_model].map(part => String(part || '').trim()).filter(Boolean).join(' ');
  const exposure = [
    image.f_number ? `f/${Number(image.f_number).toFixed(1)}` : '',
    image.exposure_time ? `${image.exposure_time} 秒` : '',
    image.iso ? `ISO ${image.iso}` : '',
    image.focal_length ? `${Number(image.focal_length).toFixed(0)} mm` : ''
  ].filter(Boolean).join(' · ');
  return [
    { key: 'taken', label: '拍摄时间', value: image.taken_at ? formatTakenAt(image.taken_at) : '没有记录' },
    { key: 'camera', label: '相机', value: camera },
    { key: 'lens', label: '镜头', value: String(image.lens_model || '').trim() },
    { key: 'exposure', label: '参数', value: exposure },
    { key: 'size', label: '尺寸', value: image.width && image.height ? `${image.width} × ${image.height}` : '' },
    { key: 'folder', label: '文件夹', value: photoFolderName(image.directory), title: String(image.directory || '') }
  ].filter(fact => fact.value);
}

function errorText(err) {
  return translateBackendError(err?.message || err);
}

export default {
  name: 'PhotoLightbox',
  components: { AppBanner, AppIcon, AppSpinner, DialogShell, RatingStars, TagPill, WallpaperButton },
  props: {
    image: { type: Object, required: true },
    hasPrev: { type: Boolean, default: false },
    hasNext: { type: Boolean, default: false },
    // 正在为「下一张」续载下一批；续载失败的原因（宿主的翻页错误）。
    loadingNext: { type: Boolean, default: false },
    nextError: { type: String, default: '' }
  },
  emits: ['close', 'prev', 'next', 'patch', 'changed', 'open-person', 'open-in-manage'],
  data() {
    return {
      imageFailed: false,
      detail: { loading: false, error: '', people: [] },
      favoriteBusy: false,
      ratingBusy: false,
      actionError: ''
    };
  },
  computed: {
    originalURL() { return photoOriginalURL(this.image.id); },
    facts() { return photoLightboxFacts(this.image); },
    rating() {
      const value = this.image?.personal_rating;
      return value === null || value === undefined ? null : Number(value);
    }
  },
  watch: {
    // 换了一张：这一张的出错状态、人物与查看计时从头来。
    'image.id': {
      immediate: true,
      handler(id) {
        this.imageFailed = false;
        this.actionError = '';
        this.loadDetail(Number(id));
        this.scheduleViewRecord(Number(id));
      }
    }
  },
  mounted() {
    document.addEventListener('keydown', this.handleKeydown);
  },
  beforeUnmount() {
    document.removeEventListener('keydown', this.handleKeydown);
    clearTimeout(this._viewTimer);
    this._detailToken = (this._detailToken || 0) + 1;
  },
  methods: {
    async loadDetail(id) {
      const token = this._detailToken = (this._detailToken || 0) + 1;
      this.detail = { loading: true, error: '', people: [] };
      if (!id) return;
      try {
        const detail = await GetImageDetail(id);
        if (token !== this._detailToken) return;
        this.detail = { loading: false, error: '', people: (detail?.people || []).filter(item => item?.person?.id) };
        // 详情里的图片是库里此刻的样子（拍摄信息、收藏、评分），并进本页这一张。
        if (detail?.image && Number(detail.image.id) === id) this.$emit('patch', { ...detail.image, id });
      } catch (err) {
        if (token === this._detailToken) this.detail = { loading: false, error: `人物没有读到：${errorText(err)}`, people: [] };
      }
    },
    scheduleViewRecord(id) {
      clearTimeout(this._viewTimer);
      this._viewTimer = null;
      if (!id) return;
      this._viewTimer = setTimeout(() => {
        this._viewTimer = null;
        if (Number(this.image?.id) !== id || this.imageFailed) return;
        this.recordView(id);
      }, VIEW_RECORD_DELAY_MS);
    },
    async recordView(id) {
      try {
        const result = await RecordImageView(id);
        if (result?.recorded) this.$emit('patch', { id, view_count: result.view_count, last_viewed_at: result.last_viewed_at });
      } catch (err) {
        logFrontend('PhotoLightbox', 'RecordImageView failed', { imageID: id, err: String(err) }, true);
      }
    },
    // 收藏点了先改本页这一张再存；存失败改回原值，在信息面板上说原因。
    async toggleFavorite() {
      const id = Number(this.image?.id || 0);
      if (!id || this.favoriteBusy) return;
      const previous = Boolean(this.image.is_favorite);
      this.favoriteBusy = true;
      this.actionError = '';
      this.$emit('patch', { id, is_favorite: !previous });
      try {
        const updated = await SetImageFavorite(id, !previous);
        const changes = { id, is_favorite: Boolean(updated?.is_favorite ?? !previous) };
        if (updated && 'favorited_at' in updated) changes.favorited_at = updated.favorited_at;
        this.$emit('patch', changes);
        this.$emit('changed', changes);
      } catch (err) {
        this.$emit('patch', { id, is_favorite: previous });
        if (Number(this.image?.id) === id) this.actionError = `收藏没有改成：${errorText(err)}`;
      } finally {
        this.favoriteBusy = false;
      }
    },
    // 评分点一下即存（null 清除），与收藏同样先改后存、失败改回。
    async saveRating(value) {
      const id = Number(this.image?.id || 0);
      if (!id || this.ratingBusy || value === this.rating) return;
      const previous = this.rating;
      this.ratingBusy = true;
      this.actionError = '';
      this.$emit('patch', { id, personal_rating: value });
      try {
        const updated = await SetImageRating(id, value);
        const changes = { id, personal_rating: updated?.personal_rating ?? null };
        this.$emit('patch', changes);
        this.$emit('changed', changes);
      } catch (err) {
        this.$emit('patch', { id, personal_rating: previous });
        if (Number(this.image?.id) === id) this.actionError = `评分没有保存：${errorText(err)}`;
      } finally {
        this.ratingBusy = false;
      }
    },
    // 本层是不是最上面一层：同在 --z-modal 的层按 DOM 先后叠放，最后一个遮罩在最上面。
    isTopmost() {
      const overlay = this.$refs.shell?.$el;
      const overlays = document.querySelectorAll('.modal-overlay');
      return Boolean(overlay) && overlays[overlays.length - 1] === overlay;
    },
    handleKeydown(event) {
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || !this.isTopmost()) return;
      // 焦点在文本框里（全站同一判定，utils/keyboardShortcuts.js）时按键归那个控件。
      if (isTextEntryTarget(event.target)) return;
      if (event.key === 'ArrowLeft') {
        event.preventDefault();
        if (this.hasPrev) this.$emit('prev');
      } else if (event.key === 'ArrowRight') {
        event.preventDefault();
        if (this.hasNext && !this.loadingNext) this.$emit('next');
      } else if (event.key === 'f' || event.key === 'F') {
        event.preventDefault();
        this.toggleFavorite();
      }
    }
  }
};
</script>

<style scoped>
/* 全屏看图：DialogShell 铺满窗口，正文不要留白也不滚动；舞台占一行确定的高度，图片按比例缩进去
   （与图片管理查看器同一教训：行高不确定时 max-height: 100% 失效，竖图会撑出视口）。 */
:deep(.photo-lightbox) { background: var(--bg-color); }
:deep(.photo-lightbox .ui-dialog__head) { border-bottom: 1px solid var(--hairline-faint); }
:deep(.photo-lightbox .ui-dialog__body) { display: flex; padding: 0; overflow: hidden; }
.photo-lightbox__layout {
  display: grid;
  flex: 1 1 auto;
  grid-template-columns: minmax(0, 1fr) min(360px, 38vw);
  grid-template-rows: minmax(0, 1fr);
  min-width: 0;
  min-height: 0;
}
.photo-lightbox__stage {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  padding: var(--space-5) 64px;
  background: var(--overlay-strong);
}
.photo-lightbox__img { max-width: 100%; max-height: 100%; object-fit: contain; border-radius: var(--radius-sm); }
.photo-lightbox__fallback {
  display: grid;
  justify-items: center;
  gap: var(--space-2);
  max-width: 420px;
  padding: var(--space-5);
  border: 1px dashed var(--recap-hero-chip-border);
  border-radius: var(--radius);
  color: var(--recap-hero-muted);
  text-align: center;
}
.photo-lightbox__fallback p { margin: 0; font-size: var(--fs-secondary); line-height: 1.6; }
.photo-lightbox__nav {
  position: absolute;
  top: 50%;
  z-index: 2;
  width: 44px;
  height: 44px;
  border-radius: var(--radius-pill);
  background: var(--recap-hero-chip);
  color: var(--recap-hero-fg);
  transform: translateY(-50%);
}
.photo-lightbox__nav:hover:not(:disabled) { background: var(--recap-hero-chip-border); color: var(--recap-hero-fg); transform: translateY(-50%); }
.photo-lightbox__nav:disabled { opacity: 0.35; cursor: default; }
.photo-lightbox__nav--prev { left: var(--space-3); }
.photo-lightbox__nav--next { right: var(--space-3); }
.photo-lightbox__next-error { position: absolute; right: var(--space-4); bottom: var(--space-4); left: var(--space-4); z-index: 3; }
.photo-lightbox__info {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  min-width: 0;
  padding: var(--space-4) var(--space-5) var(--space-5);
  border-left: 1px solid var(--hairline-faint);
  background: var(--panel-bg);
  overflow-y: auto;
}
/* 内容比视口高时整栏滚动，不许把按钮与人物压扁。 */
.photo-lightbox__info > * { flex-shrink: 0; }
.photo-lightbox__actions { display: grid; gap: var(--space-3); justify-items: start; }
.photo-lightbox__favorite--on { border-color: var(--warning-border); background: var(--warning-soft); color: var(--warning-text); }
.photo-lightbox__favorite--on .ui-icon { fill: currentColor; }
.photo-lightbox__facts { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: var(--space-2) var(--space-3); margin: 0; font-size: var(--fs-secondary); }
.photo-lightbox__facts dt { color: var(--text-muted); }
.photo-lightbox__facts dd { margin: 0; min-width: 0; overflow: hidden; color: var(--text-primary); text-overflow: ellipsis; white-space: nowrap; }
.photo-lightbox__block { display: grid; gap: var(--space-2); padding-top: var(--space-4); border-top: 1px solid var(--hairline-faint); }
.photo-lightbox__block h3 { margin: 0; color: var(--text-secondary); font-size: var(--fs-secondary); font-weight: var(--fw-semibold); }
.photo-lightbox__people { display: flex; flex-wrap: wrap; gap: var(--space-2); }
.photo-lightbox__muted { display: flex; align-items: center; gap: var(--space-2); margin: 0; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-lightbox__manage { justify-self: start; }
@media (max-width: 900px) {
  .photo-lightbox__layout { grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(0, 1fr) auto; }
  .photo-lightbox__stage { padding: var(--space-4) 56px; }
  .photo-lightbox__info { max-height: 42vh; border-top: 1px solid var(--hairline-faint); border-left: 0; }
}
</style>
