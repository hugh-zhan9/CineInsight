import { formatEditTime, preflightSource } from '../../utils/videoEdit.js';

// 各编辑器共用的来源信息：名字取片库记录，时长优先取预检（ffprobe 实测，毫秒），
// 没有预检时退回片库记录里的秒数。videos 是页面按配方取回的 { [id]: Video }。
export const workbenchVideoMixin = {
  props: {
    videos: { type: Object, default: () => ({}) }
  },
  methods: {
    videoName(videoID) {
      return this.videos?.[videoID]?.name || `视频 #${videoID}`;
    },
    durationMs(videoID) {
      const probed = Number(preflightSource(this.preflight, videoID)?.duration_ms || 0);
      if (probed > 0) return probed;
      return Math.round(Number(this.videos?.[videoID]?.duration || 0) * 1000);
    },
    durationText(videoID) {
      const ms = this.durationMs(videoID);
      return ms > 0 ? `时长 ${formatEditTime(ms)}` : '';
    }
  }
};
