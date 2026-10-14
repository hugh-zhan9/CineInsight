<template>
  <!-- 访问 PIN 页（D-PC45、PLAY-01；设计稿「访问 PIN」）：服务端对任何数据请求回 401 pin_required 时显示。
       输入框不设 maxlength：超长的 PIN 被静默截断，用户会以为输的是整串。默认遮住，可以点眼睛看一眼。 -->
  <section class="pin-gate" data-test="short-feed-pin-gate">
    <form class="pin-gate__card" @submit.prevent="submit">
      <div class="pin-gate__intro">
        <span class="pin-gate__eyebrow">析微影策 · 手机端</span>
        <h1>输入访问 PIN</h1>
        <p class="pin-gate__hint">PIN 在电脑端「设置 › 手机端浏览」里设置。登录后 30 天内这台手机不用再输。</p>
      </div>
      <div class="pin-gate__field">
        <label class="pin-gate__label" for="short-feed-pin">PIN</label>
        <div class="pin-gate__box" :class="{ 'pin-gate__box--error': error }">
          <input
            id="short-feed-pin"
            ref="input"
            v-model="pin"
            class="pin-gate__input"
            :type="pinVisible ? 'text' : 'password'"
            autocomplete="current-password"
            placeholder="至少 6 位"
            :aria-describedby="error ? 'short-feed-pin-error' : undefined"
            data-test="short-feed-pin-input"
            :disabled="busy"
          />
          <button
            type="button"
            class="btn-icon pin-gate__reveal"
            :aria-label="pinVisible ? '隐藏 PIN' : '显示 PIN'"
            :aria-pressed="pinVisible ? 'true' : 'false'"
            data-test="short-feed-pin-reveal"
            @click="pinVisible = !pinVisible"
          >
            <svg class="top-icon top-icon--small" viewBox="0 0 24 24" aria-hidden="true">
              <path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12Z" />
              <circle cx="12" cy="12" r="3" />
              <path v-if="pinVisible" d="m4 4 16 16" />
            </svg>
          </button>
        </div>
        <p v-if="error" id="short-feed-pin-error" class="pin-gate__error" role="alert" data-test="short-feed-pin-error">{{ error }}</p>
      </div>
      <button type="submit" class="btn btn--primary pin-gate__submit" data-test="short-feed-pin-submit" :disabled="busy">{{ busy ? '验证中…' : '进入' }}</button>
    </form>
  </section>
</template>

<script>
import { authenticate } from '../api.js';
import { feedErrorText, pinInputProblem } from '../errors.js';

export default {
  name: 'PinGate',
  emits: ['authenticated'],
  data() {
    return { pin: '', busy: false, error: '', pinVisible: false };
  },
  mounted() {
    this.$refs.input?.focus?.();
  },
  methods: {
    async submit() {
      if (this.busy) return;
      const problem = pinInputProblem(this.pin);
      if (problem) {
        this.error = problem;
        return;
      }
      this.busy = true;
      this.error = '';
      try {
        await authenticate(this.pin);
        this.pin = '';
        this.$emit('authenticated');
      } catch (err) {
        this.error = feedErrorText(err);
      } finally {
        this.busy = false;
      }
    }
  }
};
</script>
