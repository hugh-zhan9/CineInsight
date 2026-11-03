import { GetVideoChapterSets, ListChapterSets, SaveVideoChapters, ResolveChapter, PrepareChapterDraft, StartChapterDraft, ListChapterDraftJobs, GetChapterDraft, CancelChapterDraft, DiscardChapterDraft, PreviewChapterApplication, ApplyChapterDraft, DiscardChapterApplication, CancelAllChapterDrafts, RefreshVideoTechnicalMetadata } from '../../wailsjs/go/main/App';
import { experienceData } from './experienceResult.js';
export const chapterAPI = {
  sets: async id => experienceData(await GetVideoChapterSets(id)),
  library: async request => experienceData(await ListChapterSets(request)),
  save: async request => experienceData(await SaveVideoChapters(request)),
  resolve: async request => experienceData(await ResolveChapter(request)),
  prepare: async request => experienceData(await PrepareChapterDraft(request)),
  start: async request => experienceData(await StartChapterDraft(request)),
  jobs: async request => experienceData(await ListChapterDraftJobs(request)),
  draft: async id => experienceData(await GetChapterDraft(id)),
  cancel: async (id,revision) => experienceData(await CancelChapterDraft(id,revision)),
  discard: async (id,revision) => experienceData(await DiscardChapterDraft(id,revision)),
  preview: async request => experienceData(await PreviewChapterApplication(request)),
  apply: async token => experienceData(await ApplyChapterDraft(token)),
  discardPreview: async token => experienceData(await DiscardChapterApplication(token)),
  cancelAll: async () => experienceData(await CancelAllChapterDrafts()),
  probe: id => RefreshVideoTechnicalMetadata(id)
};
export const CHAPTER_JOB_LABELS = { queued:'排队中',running:'生成中',draft_ready:'待校对',applied:'已采纳',cancelled:'已取消',discarded:'已放弃',obsolete:'输入已变化，保留阅读',failed:'生成失败',interrupted:'已中断',job_compacted:'历史请求已处理' };
export const CHAPTER_SOURCE_LABELS = { ready:'原片版本一致',unread:'尚未读取章节',invalid:'原文件章节不可解析，其他技术信息仍可使用',unavailable:'原片不可用，保留阅读',changed:'原片已变化，请重新核对位置' };
export function chapterTime(ms) { return `${(Number(ms)/1000).toFixed(3)} 秒`; }
export function chapterEditorInputs(rows) {
  return rows.map(row => {
    const start = Number(row.startSeconds)*1000, end = Number(row.endSeconds)*1000;
    if (row.startSeconds === '' || row.endSeconds === '' || !Number.isSafeInteger(Math.round(start)) || !Number.isSafeInteger(Math.round(end)) || Math.abs(start-Math.round(start)) >= 0.00001 || Math.abs(end-Math.round(end)) >= 0.00001 || start < 0 || end <= start) throw new Error('请输入毫秒精度的有效起止时间');
    return {id:row.id || 0,title:row.title,summary:row.summary || '',start_ms:Math.round(start),end_ms:Math.round(end)};
  });
}

export const CHAPTER_ERROR_LABELS = {summary_timeout:'生成超时，本次失败；可重新核对范围后手动生成。',summary_failed:'生成失败，原人工章节保留；请检查服务后手动重新生成。',ai_output_invalid:'模型输出未通过证据校验，原人工章节保留。',input_changed:'原片、字幕或人工章节已变化，旧草稿仅供阅读。',cancelled:'生成已取消，原人工章节保留。',interrupted:'任务已中断，不会自动再次调用模型。'};
