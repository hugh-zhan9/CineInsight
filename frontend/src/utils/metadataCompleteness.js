// 元数据完整度（D-MC-02）与「缺失某项」筛选（D-MC-03）的项与中文名。
// 取值与后端 services/metadata_completeness.go 的 videoMissingSQL / imageMissingSQL 键一一对应。

// 片库「缺少…」下拉：视频项里除缩略图与两个本地资料待处理项之外的全部。
export const VIDEO_MISSING_OPTIONS = [
  { value: 'title', label: '显示标题' },
  { value: 'year', label: '年份' },
  { value: 'description', label: '简介' },
  { value: 'poster', label: '海报' },
  { value: 'fanart', label: '背景图' },
  { value: 'people', label: '人物' },
  { value: 'tags', label: '标签' },
  { value: 'subtitles', label: '字幕' },
  { value: 'technical', label: '技术信息' }
];

// 图片库「缺少…」下拉。拍摄时间与地理位置只看解析过 EXIF 的图片。
export const IMAGE_MISSING_OPTIONS = [
  { value: 'taken_at', label: '拍摄时间' },
  { value: 'gps', label: '地理位置' },
  { value: 'tags', label: '标签' },
  { value: 'people', label: '人物' }
];

// 完整度页签的额外项：缩略图只统计不进列表筛选；两个待处理项走现有的本地资料视图。
export const COMPLETENESS_EXTRA_LABELS = {
  thumbnail: '缩略图',
  nfo_update: '本地资料有更新',
  nfo_error: '本地资料读取出错'
};

export function missingFieldLabel(kind, field) {
  const options = kind === 'image' ? IMAGE_MISSING_OPTIONS : VIDEO_MISSING_OPTIONS;
  return options.find(option => option.value === field)?.label || COMPLETENESS_EXTRA_LABELS[field] || field;
}

// 能直接打开「缺失某项」列表的项（缩略图与待处理项不能）。
export function isMissingFilterField(kind, field) {
  const options = kind === 'image' ? IMAGE_MISSING_OPTIONS : VIDEO_MISSING_OPTIONS;
  return options.some(option => option.value === field);
}
