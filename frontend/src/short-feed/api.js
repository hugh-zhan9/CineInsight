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

export function getNextItem(excludeKeys = []) {
  const query = excludeKeys.length > 0 ? `?exclude=${excludeKeys.join(',')}` : '';
  return requestJSON(`/short-api/feed/next${query}`);
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
