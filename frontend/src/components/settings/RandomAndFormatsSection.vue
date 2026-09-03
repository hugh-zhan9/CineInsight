<template>
  <!-- 智能随机播放设置 -->
  <div :id="`settings-random`" class="settings-section">
    <h3>智能随机播放</h3>
    <div class="setting-item">
      <label>播放权重（1次普通播放 = N次随机播放）</label>
      <div class="setting-control-row">
        <input 
          type="number" 
          v-model.number="form.play_weight" 
          min="0.1" 
          max="10" 
          step="0.1"
          class="number-input"
        />
      </div>
      <p class="help-text">建议值: 1.0-3.0，默认2.0。权重越高，普通播放对随机选择的影响越大。</p>
    </div>
    <div class="setting-item">
      <label>播放记录半衰期（天）</label>
      <input type="number" v-model.number="form.random_half_life_days" min="0" max="3650" step="1" class="number-input" />
      <p class="help-text">默认 90 天；久未播放的视频会逐渐回到随机池。设为 0 时关闭衰减并完全使用原算法。</p>
    </div>
  </div>

  <!-- 视频格式设置 -->
  <div :id="`settings-video-formats`" class="settings-section">
    <h3>支持的视频格式</h3>
    <div class="setting-item">
      <textarea
        v-model="form.video_extensions"
        rows="3"
        class="text-input settings-textarea"
        placeholder=".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt"
      ></textarea>
      <p class="help-text">用逗号分隔，留空则使用默认配置。</p>
    </div>
  </div>

  <!-- 图片格式设置 -->
  <div :id="`settings-image-formats`" class="settings-section">
    <h3>支持的图片格式</h3>
    <div class="setting-item">
      <textarea
        v-model="form.image_extensions"
        rows="3"
        class="text-input settings-textarea"
        data-test="image-extensions"
        placeholder=".jpg,.jpeg,.png,.gif,.webp,.heic,.heif,.dng,.cr2,.cr3,.nef,.arw,.orf,.raf,.rw2"
      ></textarea>
      <p class="help-text">用逗号分隔，留空则回退默认清单。</p>
    </div>
  </div>
</template>

<script>
// 智能随机播放与视频/图片格式三个分区：纯表单字段。
export default {
  name: 'RandomAndFormatsSection',
  props: {
    form: { type: Object, required: true }
  }
};
</script>

<style scoped>

</style>
