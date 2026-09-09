async function requestJSON(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options
  });
  let payload = null;
  const contentType = response.headers.get('content-type') || '';
  if (contentType.includes('application/json')) {
    payload = await response.json();
  }
  if (!response.ok) {
    const message = payload?.message || payload?.error || response.statusText;
    throw new Error(message);
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

export function recordPlay(item) {
  return postJSON(itemPath(item, 'play'), { source: 'short_feed' });
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
