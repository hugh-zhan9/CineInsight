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

export function getNextVideo(excludeIDs = []) {
  const query = excludeIDs.length > 0 ? `?exclude=${excludeIDs.join(',')}` : '';
  return requestJSON(`/short-api/feed/next${query}`);
}

export function recordPlay(videoID) {
  return postJSON(`/short-api/videos/${videoID}/play`, { source: 'short_feed' });
}

export function setLiked(videoID, liked) {
  return postJSON(`/short-api/videos/${videoID}/like`, { liked });
}

export function setFavorited(videoID, favorited) {
  return postJSON(`/short-api/videos/${videoID}/favorite`, { favorited });
}

export function deleteVideo(videoID) {
  return postJSON(`/short-api/videos/${videoID}/delete`, { confirm_move_to_trash: true });
}

export function getFavorites() {
  return requestJSON('/short-api/favorites');
}
