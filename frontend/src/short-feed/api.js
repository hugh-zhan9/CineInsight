// 请求失败统一抛 FeedRequestError：页面按 code 与 status 区分提示（PIN、锁定、磁盘离线、网络异常），
// 不再只拿一句 message。network 为 true 表示请求根本没到服务端或响应没读完。
export class FeedRequestError extends Error {
  constructor(message, { status = 0, code = '', payload = null, network = false } = {}) {
    super(message);
    this.name = 'FeedRequestError';
    this.status = status;
    this.code = code;
    this.payload = payload;
    this.network = network;
  }
}

// 服务端设了 PIN 而当前没有会话时，所有数据接口都回 401 pin_required（D-PC45）。
// 页面登记一个回调，任何一次请求撞上它就切到 PIN 页，不必每个调用点各判一遍。
let authRequiredHandler = null;
export function setAuthRequiredHandler(handler) {
  authRequiredHandler = typeof handler === 'function' ? handler : null;
}

async function requestJSON(path, options = {}) {
  let response;
  let payload = null;
  try {
    response = await fetch(path, {
      credentials: 'same-origin',
      ...options
    });
    const contentType = response.headers.get('content-type') || '';
    if (contentType.includes('application/json')) {
      payload = await response.json();
    }
  } catch (_err) {
    throw new FeedRequestError('网络异常', { network: true });
  }
  if (!response.ok) {
    const code = payload?.code || payload?.error || '';
    const message = payload?.message || payload?.error || response.statusText;
    const error = new FeedRequestError(message, { status: response.status, code, payload });
    if (response.status === 401 && code === 'pin_required') authRequiredHandler?.(error);
    throw error;
  }
  return payload;
}

function postJSON(path, body) {
  return requestJSON(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
}

// 图片 ID 与视频 ID 各自从 1 开始，所以最近列表与媒体地址都必须带类型。
export function itemKey(item) {
  return item ? `${item.media_kind}:${item.id}` : '';
}

function itemPath(item, action) {
  return `/short-api/items/${item.media_kind}/${item.id}/${action}`;
}

export function getNextItem(excludeKeys = [], scope = 'all', media = 'all') {
  const params = new URLSearchParams();
  if (excludeKeys.length > 0) params.set('exclude', excludeKeys.join(','));
  if (scope && scope !== 'all') params.set('scope', scope);
  if (media && media !== 'all') params.set('media', media);
  const query = params.toString();
  return requestJSON(`/short-api/feed/next${query ? `?${query}` : ''}`);
}

export function recordPlay(item, viewSessionID = '') {
  return postJSON(itemPath(item, 'play'), { source: 'short_feed', ...(viewSessionID ? { view_session_id: viewSessionID } : {}) });
}

export function setLiked(item, liked) {
  return postJSON(itemPath(item, 'like'), { liked });
}

export function setFavorited(item, favorited) {
  return postJSON(itemPath(item, 'favorite'), { favorited });
}

export function deleteItem(item) {
  return postJSON(itemPath(item, 'delete'), { confirm_move_to_trash: true });
}

export function getFavorites() {
  return requestJSON('/short-api/favorites');
}

export function getScopes(media = 'all') {
  const query = media && media !== 'all' ? `?media=${encodeURIComponent(media)}` : '';
  return requestJSON(`/short-api/feed/scopes${query}`);
}

// 标签搜索实时查服务端：关键词随每次输入一起发过去，由服务端在全量标签上筛。
export function getFeedTags(keyword = '') {
  const trimmed = String(keyword || '').trim();
  const query = trimmed ? `?q=${encodeURIComponent(trimmed)}` : '';
  return requestJSON(`/short-api/tags${query}`);
}

export function createFeedTag(name) {
  return postJSON('/short-api/tags', { name });
}

export function setRating(item, rating) {
  return postJSON(itemPath(item, 'rating'), { rating });
}

export function setWatched(item, watched) {
  return postJSON(itemPath(item, 'watched'), { watched });
}

export function setItemTag(item, tagID, attached) {
  return postJSON(itemPath(item, 'tag'), { tag_id: tagID, attached });
}

export function restoreItem(item) {
  return postJSON(itemPath(item, 'restore'), {});
}

// 用 PIN 换会话 Cookie（D-PC45）：成功后服务端 Set-Cookie，之后的数据请求与 <video>/<img> 同源自动带上。
export function authenticate(pin) {
  return postJSON('/short-api/auth', { pin });
}
