// ⌘K 统一搜索（D-P2-05，详细设计 §3.4）：命令面板在命令之外同时搜的几组——视频、人物、作品集、台词、画面，
// 以及粘贴或拖入一张图时的「以图」。这里只编排既有接口（不加后端搜索），把各组结果整理成面板能直接画的条目，
// 并给出各组「查看全部」去哪一页、带什么参数。状态（每组的加载中 / 出错 / 结果）在 CommandPalette 里。
import { ListCollections, ListPeople, SearchLibraryVideoPage, SearchScenes, SearchScenesByImage } from '../../wailsjs/go/main/App';
import { formatDuration } from './mediaDetails.js';
import { videoArtwork } from './recap.js';
import {
  SCENE_IMAGE_MAX_BYTES,
  SCENE_IMAGE_TYPES,
  formatSceneRange,
  formatSceneTime,
  sceneErrorText,
  sceneFrameURL,
  sceneNoticeText,
  scenePlaybackStartMs
} from './sceneSearch.js';

// 输入停 250 毫秒才发请求；命令以外的组至少 2 个字才搜（命令照旧随输入即时过滤）。
export const UNIFIED_SEARCH_DEBOUNCE_MS = 250;
export const UNIFIED_SEARCH_MIN_LENGTH = 2;
// 每组最多显示 5 条：面板是「快速跳过去」，看更多走组标题旁的「查看全部」。
export const UNIFIED_GROUP_LIMIT = 5;
// 以图按设计多取几条（8），同一部片相邻的几段常常一起命中，显示时仍只留前 5 条。
export const UNIFIED_IMAGE_LIMIT = 8;
// 粘贴或拖入的图先缩到长边 1024 再编码：苹果图像特征用不到更大的图，传给后端的数据也小得多。
export const UNIFIED_IMAGE_MAX_EDGE = 1024;

// 组的固定顺序（命令组在这些之前）。noun 用在空状态「没有匹配的…」里。
export const UNIFIED_SEARCH_GROUPS = [
  { key: 'video', label: '视频', noun: '视频' },
  { key: 'person', label: '人物', noun: '人物' },
  { key: 'collection', label: '作品集', noun: '作品集' },
  { key: 'dialogue', label: '台词', noun: '台词' },
  { key: 'visual', label: '画面', noun: '画面' },
  { key: 'image', label: '以图', noun: '相似画面' }
];

// 跟着文字查询走的组（以图跟着图片走，不在其中）。
export const TEXT_GROUP_KEYS = ['video', 'person', 'collection', 'dialogue', 'visual'];

const SCENE_GROUP_KEYS = new Set(['dialogue', 'visual', 'image']);

// 文字查询够不够搜命令以外的组：去掉首尾空白后至少 2 个字。
export function searchableKeyword(query) {
  const keyword = String(query ?? '').trim();
  return keyword.length >= UNIFIED_SEARCH_MIN_LENGTH ? keyword : '';
}

// 按组记请求序号：新的查询、清空、单组重试都会给这一组换一个序号，响应回来时序号已经不是最新的就丢掉，
// 晚到的旧结果不会盖住新的。各组的序号互不影响（一组重试不作废其他组正在路上的请求）。
export function createGroupSequencer() {
  const current = new Map();
  return {
    next(key) {
      const value = (current.get(key) || 0) + 1;
      current.set(key, value);
      return value;
    },
    isCurrent(key, value) {
      return current.get(key) === value;
    },
    invalidate(keys) {
      for (const key of keys) this.next(key);
    }
  };
}

function videoRequest(keyword) {
  return {
    filter: {
      search_mode: 'file',
      keyword,
      smart_view: '',
      tag_ids: [],
      min_size: 0,
      max_size: 0,
      min_height: 0,
      max_height: 0,
      min_rating: null,
      max_rating: null,
      sort_mode: 'balanced',
      // 版本组合并成一部：同一部片的几个版本在面板里只占一行。
      collapse_versions: true
    },
    limit: UNIFIED_GROUP_LIMIT
  };
}

function videoTitle(video) {
  return video?.display_title || video?.name || `视频 ${video?.id}`;
}

export function normalizeVideoResults(page) {
  return (page?.videos || []).filter(video => Number(video?.id) > 0).slice(0, UNIFIED_GROUP_LIMIT).map(video => ({
    id: `video:${video.id}`,
    title: videoTitle(video),
    subtitle: [Number(video.year) > 0 ? String(video.year) : '', formatDuration(video.duration)].filter(Boolean).join(' · '),
    media: { url: videoArtwork(video.id, 'poster'), shape: 'poster' },
    target: { type: 'video', video }
  }));
}

export function normalizePeopleResults(list) {
  return (list || []).filter(item => Number(item?.person?.id) > 0).slice(0, UNIFIED_GROUP_LIMIT).map(item => {
    const person = item.person;
    const counts = [`${Number(item.active_video_count || 0)} 部视频`];
    if (Number(item.active_image_count) > 0) counts.push(`${Number(item.active_image_count)} 张图片`);
    return {
      id: `person:${person.id}`,
      title: person.display_name || person.original_name || `人物 ${person.id}`,
      subtitle: counts.join(' · '),
      media: { url: item.avatar_url || '', shape: 'avatar' },
      target: { type: 'entity', entityType: 'person', id: Number(person.id), name: person.display_name || '' }
    };
  });
}

export function normalizeCollectionResults(list) {
  return (list || []).filter(item => Number(item?.collection?.id) > 0).slice(0, UNIFIED_GROUP_LIMIT).map(item => {
    const collection = item.collection;
    return {
      id: `collection:${collection.id}`,
      title: collection.name || `作品集 ${collection.id}`,
      subtitle: `${Number(item.active_video_count || 0)} 部视频`,
      media: { url: item.cover_url || '', shape: 'poster' },
      target: { type: 'entity', entityType: 'collection', id: Number(collection.id), name: collection.name || '' }
    };
  });
}

// 场景命中（台词、画面、以图）：台词以那句话为主、片名与时间为辅；画面以片名为主、时间区间为辅。
// 打开时提前 2 秒起播（与场景检索页同一口径）。同一部片同一时刻的重复命中只留一条。
export function normalizeSceneResults(groupKey, result) {
  const seen = new Set();
  const items = [];
  for (const hit of result?.hits || []) {
    const videoID = Number(hit?.video_id);
    if (!(videoID > 0)) continue;
    const startMs = Math.max(0, Math.round(Number(hit.start_ms) || 0));
    const id = `${groupKey}:${videoID}:${startMs}`;
    if (seen.has(id)) continue;
    seen.add(id);
    const title = hit.title || `视频 ${videoID}`;
    const dialogue = groupKey === 'dialogue' && hit.text;
    items.push({
      id,
      title: dialogue ? hit.text : title,
      subtitle: dialogue ? `${title} · ${formatSceneTime(startMs)}` : formatSceneRange(startMs, hit.end_ms),
      label: `${title} ${formatSceneTime(startMs)}`,
      media: { url: sceneFrameURL(videoID, startMs, 160), shape: 'frame' },
      target: { type: 'scene', videoID, startMs: scenePlaybackStartMs(startMs) }
    });
    if (items.length >= UNIFIED_GROUP_LIMIT) break;
  }
  return { items, notices: (result?.notices || []).filter(Boolean) };
}

// 发起一组搜索并整理结果：{ items, notices }。input = { keyword } 或 { image: { base64 } }。
// 失败直接抛给调用方（面板把它记成这一组的错误，其他组不受影响）。
export async function searchGroup(key, input = {}) {
  switch (key) {
    case 'video':
      return { items: normalizeVideoResults(await SearchLibraryVideoPage(videoRequest(input.keyword))), notices: [] };
    case 'person':
      return { items: normalizePeopleResults(await ListPeople(input.keyword, '', 0, UNIFIED_GROUP_LIMIT)), notices: [] };
    case 'collection':
      return { items: normalizeCollectionResults(await ListCollections(input.keyword, '', 0, UNIFIED_GROUP_LIMIT)), notices: [] };
    case 'dialogue':
    case 'visual':
      return normalizeSceneResults(key, await SearchScenes({ query: input.keyword, mode: key, limit: UNIFIED_GROUP_LIMIT }));
    case 'image':
      return normalizeSceneResults(key, await SearchScenesByImage({ image_base64: input.image?.base64 || '', video_id: 0, at_ms: 0, limit: UNIFIED_IMAGE_LIMIT }));
    default:
      throw new Error(`未知的搜索分组：${key}`);
  }
}

// 一组失败时给人看的一句话：场景三组认得场景检索的错误代码，其余给后端原话（去掉 "Error: " 前缀）。
export function groupErrorText(key, err) {
  if (SCENE_GROUP_KEYS.has(key)) return sceneErrorText(err);
  return String(err?.message || err || '').trim() || '搜索失败';
}

// 场景三组没执行或没有可搜内容时的那一行说明（索引没建、模型没准备好……），只取第一条提示。
export function groupNoticeText(notices) {
  const [code] = notices || [];
  return code ? sceneNoticeText(code) : '';
}

// 组标题旁「查看全部」去哪：视频 → 视频页搜索；人物、作品集 → 各自的观看页；台词、画面 → 场景检索对应模式；
// 以图 → 场景检索，带上这张图。返回 { page, params }，由 App 的 navigateTo(page, params) 接收。
export function viewAllTarget(key, { keyword = '', image = null } = {}) {
  switch (key) {
    case 'video': return { page: 'videos', params: { keyword } };
    case 'person': return { page: 'people', params: { keyword } };
    case 'collection': return { page: 'collections', params: { keyword } };
    case 'dialogue':
    case 'visual': return { page: 'scenes', params: { query: keyword, mode: key } };
    case 'image': return image ? { page: 'scenes', params: { image: { dataURL: image.dataURL, name: image.name || '' } } } : null;
    default: return null;
  }
}

// 长边缩到 maxEdge 以内（等比，不放大），宽高取整且至少 1。
export function fitLongEdge(width, height, maxEdge = UNIFIED_IMAGE_MAX_EDGE) {
  const w = Math.max(1, Math.round(Number(width) || 0));
  const h = Math.max(1, Math.round(Number(height) || 0));
  const scale = Math.min(1, maxEdge / Math.max(w, h));
  return { width: Math.max(1, Math.round(w * scale)), height: Math.max(1, Math.round(h * scale)) };
}

// 图片能不能拿来搜：格式与 20 MB 上限与后端一致，先在本地挡掉（大文件不读进来）。返回给人看的原因，可用时为空串。
export function searchImageProblem(file) {
  if (!file) return '没有读到图片。';
  if (Number(file.size) > SCENE_IMAGE_MAX_BYTES) return '图片超过 20 MB，换一张小一点的图片再试。';
  if (!SCENE_IMAGE_TYPES.includes(file.type) || !(Number(file.size) > 0)) return '只能用 JPEG、PNG、WebP、GIF、BMP 图片搜索。';
  return '';
}

function decodeImageFile(file) {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const image = new Image();
    image.onload = () => {
      URL.revokeObjectURL(url);
      resolve(image);
    };
    image.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error('image_decode_failed'));
    };
    image.src = url;
  });
}

function renderScaledJPEG(image, maxEdge) {
  const { width, height } = fitLongEdge(image.naturalWidth || image.width, image.naturalHeight || image.height, maxEdge);
  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  const context = canvas.getContext('2d');
  if (!context) throw new Error('image_decode_failed');
  context.drawImage(image, 0, 0, width, height);
  return canvas.toDataURL('image/jpeg', 0.9);
}

// 把粘贴或拖入的图片变成搜索用的数据：校验 → 解码 → 长边缩到 1024 → JPEG data URL（缩略图也用它）。
// 返回 { dataURL, base64, name }；不能用时抛出带中文原因的错误（message 就是给人看的那句话）。
// decode / render 可替换，单测里不依赖浏览器的画布。
export async function prepareSearchImage(file, { decode = decodeImageFile, render = renderScaledJPEG } = {}) {
  const problem = searchImageProblem(file);
  if (problem) throw new Error(problem);
  let dataURL;
  try {
    dataURL = await render(await decode(file), UNIFIED_IMAGE_MAX_EDGE);
  } catch (_err) {
    throw new Error('这张图片读不出来，换一张再试。');
  }
  const comma = String(dataURL || '').indexOf(',');
  const base64 = comma >= 0 ? dataURL.slice(comma + 1) : '';
  if (!base64) throw new Error('这张图片读不出来，换一张再试。');
  return { dataURL, base64, name: file.name || '' };
}

// 剪贴板或拖放里的第一张图片文件（文字粘贴、拖进来的非图片文件返回 null）。
export function firstImageFile(transfer) {
  return [...(transfer?.files || [])].find(file => String(file?.type || '').startsWith('image/')) || null;
}
