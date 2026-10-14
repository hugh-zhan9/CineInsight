<template>
  <div class="app-feedback">
    <div v-if="toasts.length" class="app-toasts" role="status" aria-live="polite" data-test="app-toasts">
      <div
        v-for="toast in toasts"
        :key="toast.id"
        :class="['app-toast', `app-toast--${toast.level}`]"
        data-test="app-toast"
      >
        <span class="app-toast__text">{{ toast.message }}</span>
        <button
          v-if="toast.action"
          type="button"
          class="btn-link app-toast__action"
          data-test="app-toast-action"
          @click="runToastAction(toast.id)"
        >{{ toast.action.label }}</button>
        <button
          type="button"
          class="btn-icon btn--sm app-toast__close"
          aria-label="关闭提示"
          data-test="app-toast-close"
          @click="dismissToast(toast.id)"
        ><AppIcon name="close" :size="16" /></button>
      </div>
    </div>

    <!-- 确认框常从别的对话框里发起，那一层可能 teleport 到了 body 末尾（原图、人脸来源、台词卡…）。
         打开时才 teleport 到 body 末尾，DOM 顺序就是打开顺序，同在 --z-modal 也压得住当时最上面那层。 -->
    <Teleport v-if="confirm" to="body">
      <DialogShell
        :title="confirm.title"
        size="sm"
        class="app-confirm"
        data-test="app-confirm"
        @close="resolveConfirm(false)"
      >
        <p class="app-confirm__message">{{ confirm.message }}</p>
        <p v-if="confirm.impact" class="app-confirm__impact" data-test="app-confirm-impact">{{ confirm.impact }}</p>
        <label v-if="confirm.requiredText" class="app-confirm__typed">
          <span>输入「{{ confirm.requiredText }}」以确认，此操作无法撤销</span>
          <input
            ref="typedInput"
            v-model="typed"
            type="text"
            class="text-input"
            autocomplete="off"
            spellcheck="false"
            data-test="app-confirm-input"
            @keydown.enter="handleTypedEnter"
          >
        </label>
        <template #footer>
          <button type="button" class="btn btn--secondary" data-test="app-confirm-cancel" @click="resolveConfirm(false)">
            {{ confirm.cancelText }}
          </button>
          <button
            type="button"
            :class="['btn', confirm.danger ? 'btn--danger' : 'btn--primary']"
            :disabled="!canConfirm"
            data-test="app-confirm-ok"
            @click="confirmOk"
          >{{ confirm.confirmText }}</button>
        </template>
      </DialogShell>
    </Teleport>
  </div>
</template>

<script>
import AppIcon from './ui/AppIcon.vue';
import DialogShell from './ui/DialogShell.vue';
import { confirmTextMatches, dismissToast, feedbackState, resolveConfirm, runToastAction } from '../utils/feedback.js';

// 全局唯一的提示宿主，挂在 App 根上。业务代码只调 utils/feedback.js 里的函数，
// 不需要把提示状态一层层往下传。提示最多带一个动作（撤销 / 重试 / 查看）；确认框分两级，
// 不可逆的要原样输入按钮上的文字才能点（D-UD-07）。
export default {
  name: 'AppFeedback',
  components: { AppIcon, DialogShell },
  data() {
    return { typed: '' };
  },
  computed: {
    toasts() { return feedbackState.toasts; },
    confirm() { return feedbackState.confirm; },
    canConfirm() { return confirmTextMatches(this.confirm, this.typed); }
  },
  watch: {
    // 换了一个确认框就清空输入；不可逆确认把焦点放进输入框。
    confirm: {
      handler(next) {
        this.typed = '';
        if (next?.requiredText) this.$nextTick(() => this.$refs.typedInput?.focus());
      }
    }
  },
  methods: {
    dismissToast,
    resolveConfirm,
    runToastAction,
    confirmOk() {
      if (this.canConfirm) resolveConfirm(true);
    },
    // 输入法组字时的回车是选字上屏，不是确认（WKWebView 里组字结束那一下 keyCode 是 229）。
    handleTypedEnter(event) {
      if (event.isComposing || event.keyCode === 229) return;
      event.preventDefault();
      this.confirmOk();
    }
  }
};
</script>

<style scoped>
/* 层级（tokens.css「层级」）：确认框与其他对话框同在 --z-modal，靠打开时 teleport 排到最上面；提示条在
   --z-toast，压在所有对话框与确认框之上——删除后「撤销」提示和随后弹出的确认框同时在时，提示也点得到，
   点它不会落到确认框的遮罩上把确认框关掉。平时在右下角，有对话框、全窗口层或抽屉开着时挪开（见下面几条）。
   下沿（--toast-bottom）：平时离窗口底边 16。吸底的选择操作条看得见时（SelectionBar 把它从窗口底边往上占的高度写在
   根节点的 --selection-bar-inset 上，折成几行都量实际高度），抬到操作条上沿再往上 8。右下角与抽屉开着时的左下角都用它；
   贴顶的几种位置（对话框、看图层、字幕工作台）与版本对比不受影响。 */
.app-toasts {
  --toast-bottom: max(var(--space-4), calc(var(--selection-bar-inset, 0px) + var(--space-2)));
  position: fixed;
  z-index: var(--z-toast);
  right: var(--space-4);
  bottom: var(--toast-bottom);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  max-width: min(420px, calc(100vw - 32px));
  max-height: calc(100vh - var(--toast-bottom) - var(--space-4));
  overflow-y: auto;
}

/* 全窗口层与抽屉开着时（2026-10-14 G1）：每种层按自己的布局挑一块没有控件的地方，1024×768 与 1440×900 下都量过。
   条件统一写成 body:has(<层的类>:where(:not([style*="display: none"] *)))：
   - 层的类是各层根节点上本来就有的类（字幕工作台用编辑布局的类，见那一条），不另加状态；
   - :where(:not(…)) 排除挂在 v-show 隐藏的页面里的层：片库、图片管理、清理中心切走时只隐藏不卸载，详情抽屉、
     字幕工作台、查看器与页面里的对话框可能还开着，别的页面上的提示不能跟着挪。:where 不计特异性，对话框那条也这样写，
     各条一样重，谁生效只看先后；
   - 先后：抽屉与版本对比写在对话框那条之前，在它们上面再开对话框时让给对话框那条；看图层与字幕工作台写在之后，
     它们的位置都在对话框那条的范围（左边、中线以上）以内，上面再开对话框也让得开对话框的动作，所以照旧按自己的。
   每条都把 top / right / bottom / left / max-height 写全，几条同时命中时不会混着生效。 */

/* 抽屉（详情抽屉、任务中心）靠右停靠，里面从上到下都是控件：提示挪到另一边的左下角。人脸来源的「查看视频片段」
   也是这个抽屉，它加宽时给这一列留了位置（FaceClusterDetailDialog：宽不超过 100vw - 452）。 */
body:has(.preview-drawer:where(:not([style*="display: none"] *))) .app-toasts,
body:has(.task-center-overlay:where(:not([style*="display: none"] *))) .app-toasts {
  top: auto;
  right: auto;
  bottom: var(--toast-bottom);
  left: var(--space-4);
  max-height: calc(100vh - var(--toast-bottom) - var(--space-4));
}

/* 版本对比：顶栏一排控件、两侧画面左上 / 右上角的标注（含「生成播放代理」）在上，底部是一行播放条（45px），
   滑动分界线的把手在舞台正中。提示抬到播放条之上的右下角、只占下半截：下沿离底 64，上沿在中线以下 32。 */
body:has(.version-compare:where(:not([style*="display: none"] *))) .app-toasts {
  top: auto;
  right: var(--space-4);
  bottom: calc(var(--space-7) + var(--space-4));
  left: auto;
  max-height: calc(50vh - 96px);
}

/* 有对话框开着时（BaseModal 的遮罩 .modal-overlay 在页面里，且不在 v-show 隐藏的页面里），提示挪到左上角、只占窗口
   上半截，让开对话框的动作。对话框都居中，DialogShell 的动作是底部一排（「取消」在左、主操作在右）和右上角 ×：
   底部那一排总在窗口中线以下（带按钮的对话框最矮也比两倍底栏高），× 总在右半边，所以左上角的上半截最多压到标题或正文开头。
   留在右下角时，高的对话框连一条提示都会盖住主操作；挪到右上角压 ×、左下角压「取消」。
   层级不变，「撤销」照样点得到；放不下时提示条在上半截里滚动（top 16 + 这里的高度 = 中线以上 8px）。 */
body:has(.modal-overlay:where(:not([style*="display: none"] *))) .app-toasts {
  top: var(--space-4);
  right: auto;
  bottom: auto;
  left: var(--space-4);
  max-height: calc(50vh - 24px);
}

/* 图片管理查看器、清理查看器：× 在右上角，右边是信息栏（图片管理）或「下一张」，「上一张」贴左边、在竖直正中
   （上沿在中线以上 20 / 32），清理查看器底部一行是文件名与路径。提示贴左上角，下沿在中线以上 48。 */
body:has(.photo-viewer:where(:not([style*="display: none"] *))) .app-toasts,
body:has(.cleanup-viewer:where(:not([style*="display: none"] *))) .app-toasts {
  top: var(--space-4);
  right: auto;
  bottom: auto;
  left: var(--space-4);
  max-height: calc(50vh - 64px);
}

/* 观看侧看图（DialogShell 全窗口，本身也有 .modal-overlay）：标题栏横贯顶部（文件名与 ×，一行高 53），舞台在左、
   信息栏在右，「上一张」在舞台竖直正中（上沿在中线以下约 4）。对话框那条会压住文件名：提示从标题栏下面 72 起，
   下沿在中线以上 8。 */
body:has(.photo-lightbox:where(:not([style*="display: none"] *))) .app-toasts {
  top: calc(var(--space-7) + var(--space-5));
  right: auto;
  bottom: auto;
  left: var(--space-4);
  max-height: calc(50vh - 80px);
}

/* 字幕工作台铺满窗口（四周只留 16）。编辑布局（.subtitle-workbench__layout）在时哪儿都有控件：右下角是「保存字幕」，
   标题栏右端是「刷新」与 ×，编辑区顶上是撤销 / 重做，左栏是视频与一列工具。唯一不压控件的是左栏顶上：标题与视频画面
   的上部。提示放在那里，宽不出左栏（390），下沿 172 不到视频的控制条（左栏视频最矮是 2.39:1 的片子，画面在 94–245，
   播放键约从 180 起）：三条提示（其中一条折成两行）放得下，再多在框里滚动。
   读取中、还没有字幕、编码待转换、读取失败这几种状态页只有顶上一块（选项与按钮居中排在上面），右下角是空的，
   所以条件是编辑布局而不是工作台本身，状态页上提示照常在右下角。 */
body:has(.subtitle-workbench__layout:where(:not([style*="display: none"] *))) .app-toasts {
  top: var(--space-2);
  right: auto;
  bottom: auto;
  left: var(--space-5);
  max-width: 360px;
  max-height: 164px;
}

.app-toast {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: 10px var(--space-2) 10px var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-bg);
  box-shadow: var(--shadow-popover);
  color: var(--text-primary);
  font-size: var(--fs-secondary);
  line-height: 1.5;
}

.app-toast--error {
  border-color: var(--danger-border);
  background: var(--danger-soft);
  color: var(--danger-text);
}

.app-toast--success {
  border-color: var(--accent-border);
  background: var(--accent-soft);
  color: var(--accent-text);
}

/* 批量结果用 \n 拼多行；单条太长也不能把关闭按钮顶出屏幕。 */
.app-toast__text {
  flex: 1;
  min-width: 0;
  max-height: 40vh;
  overflow-y: auto;
  overflow-wrap: anywhere;
  white-space: pre-line;
}

/* 动作跟随提示的字色，下划线标出可点。 */
.app-toast__action {
  flex: none;
  color: inherit;
  font-weight: var(--fw-semibold);
  text-decoration: underline;
  text-underline-offset: 2px;
}

/* 比全局 .btn-icon.btn--sm（28×28，components.css 在构建产物里排在组件样式后面）多一层选择器，22×22 才生效。 */
.app-toast .app-toast__close { width: 22px; height: 22px; margin: -1px 0; color: inherit; opacity: 0.7; }
.app-toast .app-toast__close:hover:not(:disabled) { background: transparent; color: inherit; opacity: 1; }

.app-confirm__message {
  margin: 0;
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
  line-height: 1.6;
  white-space: pre-line;
}
.app-confirm__message:empty { display: none; }
.app-confirm__impact { margin: var(--space-2) 0 0; color: var(--text-primary); font-size: var(--fs-secondary); font-weight: var(--fw-medium); }
.app-confirm__typed { display: grid; gap: 6px; margin-top: var(--space-4); color: var(--text-secondary); font-size: var(--fs-caption); }
</style>
