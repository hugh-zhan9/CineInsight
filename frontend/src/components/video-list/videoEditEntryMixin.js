import { CreateEditProject } from '../../../wailsjs/go/main/App';
import { notifyError } from '../../utils/feedback.js';
import { editErrorText } from '../../utils/videoEdit.js';

// 片库里的视频工作台入口（视频编辑合同「入口」）：批量栏「合并为新视频」「批量去片头」，
// 行菜单「高清替换…」。建好项目后发 open-video-edit，由宿主切到工作台并选中它。
// 依赖片库页的 selectedVideoIds。
const SELECTION_LIMITS = {
  merge: { min: 2, max: 50, text: '合并为新视频需要选择 2–50 个视频' },
  trim_intro: { min: 1, max: 200, text: '批量去片头需要选择 1–200 个视频' }
};

export const videoEditEntryMixin = {
  data() {
    return { hdReplaceLongVideo: null, creatingEditProject: false };
  },
  methods: {
    // 合并按选中的先后顺序排来源；工作台里还能再调。
    createEditProjectFromSelection(kind) {
      const ids = [...new Set(this.selectedVideoIds.map(Number).filter(Boolean))];
      const limits = SELECTION_LIMITS[kind];
      if (!limits || ids.length < limits.min || ids.length > limits.max) {
        notifyError(limits?.text || '不支持的编辑类型');
        return Promise.resolve(null);
      }
      return this.createEditProject(kind, ids, true);
    },
    async createEditProject(kind, videoIDs, clearSelection = false) {
      if (this.creatingEditProject) return null;
      this.creatingEditProject = true;
      try {
        const project = await CreateEditProject({ kind, title: '', video_ids: videoIDs });
        if (clearSelection) this.selectedVideoIds = [];
        this.$emit('open-video-edit', project.id);
        return project;
      } catch (err) {
        notifyError(`创建编辑项目失败：${editErrorText(err)}`);
        return null;
      } finally {
        this.creatingEditProject = false;
      }
    },
    openHDReplacePicker(video) {
      this.hdReplaceLongVideo = video || null;
    },
    // 行菜单的视频是长版（主时间线），选中的是高清版。
    async createHDReplace(hdVideo) {
      const long = this.hdReplaceLongVideo;
      if (!long || !hdVideo) return;
      const project = await this.createEditProject('hd_replace', [long.id, hdVideo.id]);
      if (project) this.hdReplaceLongVideo = null;
    }
  }
};
