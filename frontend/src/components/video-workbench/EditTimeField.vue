<template>
  <input
    class="text-input edit-time"
    type="text"
    :value="text"
    :disabled="disabled"
    :aria-invalid="invalid ? 'true' : 'false'"
    :aria-label="label"
    :title="invalid ? '格式：时:分:秒.毫秒、分:秒 或秒数' : label"
    @input="onInput"
    @change="commit"
    @keydown.enter.prevent="commit"
  />
</template>

<script>
import { formatEditTime, parseEditTime } from '../../utils/videoEdit.js';

// 时间输入：显示 m:ss.mmm，认 h:mm:ss.mmm、m:ss 与秒数；只在回车或失焦时提交（commit 毫秒），
// 输入不合法时标红并保留原值，不提交。
export default {
  name: 'EditTimeField',
  props: {
    modelValue: { type: Number, default: 0 },
    disabled: { type: Boolean, default: false },
    label: { type: String, default: '' }
  },
  emits: ['commit'],
  data() {
    return { draft: null, invalid: false };
  },
  computed: {
    text() {
      return this.draft ?? formatEditTime(this.modelValue);
    }
  },
  watch: {
    modelValue() {
      this.draft = null;
      this.invalid = false;
    }
  },
  methods: {
    onInput(event) {
      this.draft = event.target.value;
    },
    commit() {
      if (this.draft === null) return;
      const ms = parseEditTime(this.draft);
      if (ms === null) {
        this.invalid = true;
        return;
      }
      this.invalid = false;
      this.draft = null;
      if (ms !== this.modelValue) this.$emit('commit', ms);
    }
  }
};
</script>
