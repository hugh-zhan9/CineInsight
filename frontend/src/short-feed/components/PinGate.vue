<template>
  <!-- 访问 PIN 页（D-PC45、PLAY-01）：服务端对任何数据请求回 401 pin_required 时显示。
       输入框不设 maxlength：超长的 PIN 被静默截断，用户会以为输的是整串。 -->
  <section class="pin-gate" data-test="short-feed-pin-gate">
    <form class="pin-gate__card" @submit.prevent="submit">
      <h1>输入访问 PIN</h1>
      <p class="pin-gate__hint">PIN 在电脑端「设置 · 手机端浏览」里设置。</p>
      <input
        ref="input"
        v-model="pin"
        class="sheet-input pin-gate__input"
        type="password"
        autocomplete="current-password"
        aria-label="访问 PIN"
        placeholder="至少 6 位"
        data-test="short-feed-pin-input"
        :disabled="busy"
      />
      <p v-if="error" class="pin-gate__error" role="alert" data-test="short-feed-pin-error">{{ error }}</p>
      <button type="submit" class="sheet-btn sheet-btn--primary pin-gate__submit" data-test="short-feed-pin-submit" :disabled="busy">{{ busy ? '验证中…' : '进入' }}</button>
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
    return { pin: '', busy: false, error: '' };
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
