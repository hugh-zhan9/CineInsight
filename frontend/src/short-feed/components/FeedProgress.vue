<template>
  <div
    class="progress-dock"
    :class="{ visible }"
    @click.stop
    @touchstart.stop
    @touchend.stop
    @wheel.stop
  >
    <div
      class="progress-scrubber"
      role="slider"
      tabindex="0"
      aria-label="播放进度"
      aria-valuemin="0"
      aria-valuemax="1000"
      :aria-valuenow="value"
      :aria-valuetext="valueText"
      @pointerdown.stop.prevent="$emit('seek-start', $event)"
      @pointermove.stop.prevent="$emit('seek-move', $event)"
      @pointerup.stop.prevent="$emit('seek-end', $event)"
      @pointercancel.stop.prevent="$emit('seek-cancel', $event)"
      @keydown.stop.prevent="$emit('seek-key', $event)"
    >
      <div class="progress-track">
        <div class="progress-fill" :style="{ width: `${value / 10}%` }"></div>
        <div class="progress-thumb" :style="{ left: `${value / 10}%` }"></div>
      </div>
    </div>
  </div>
</template>

<script>
export default {
  name: 'FeedProgress',
  props: {
    visible: { type: Boolean, default: false },
    value: { type: Number, default: 0 },
    valueText: { type: String, default: '' }
  },
  emits: ['seek-start', 'seek-move', 'seek-end', 'seek-cancel', 'seek-key']
};
</script>
