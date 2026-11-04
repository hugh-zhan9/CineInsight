import { reactive } from 'vue';
export const videoOverviewState = reactive({ video: null, session: 0, previewRequest: null });
export function openVideoOverview(video) { if (!Number.isInteger(Number(video?.id)) || Number(video.id) <= 0) return false; videoOverviewState.video = { id: Number(video.id), name: video.display_title || video.name, duration: Number(video.duration) }; videoOverviewState.session++; return true; }
export function closeVideoOverview() { videoOverviewState.video = null; }
export function requestThumbnailPreview(videoID, atMs) { if (!Number.isInteger(Number(videoID)) || Number(videoID) <= 0 || !Number.isFinite(atMs) || atMs < 0) return; videoOverviewState.previewRequest = { videoID: Number(videoID), atMs: Math.round(atMs) }; }
export function thumbnailTime(duration, ratio, samples = 48) { if (!Number.isFinite(duration) || duration <= 0 || !Number.isFinite(ratio)) return null; const bucket = Math.min(samples - 1, Math.floor(Math.max(0, Math.min(1, ratio)) * samples)); return Math.round(duration * bucket / samples * 1000); }

// 一次只读取一张，指针移动合并为最新位置；失活可取消且迟到Blob不会保留。
export function createThumbnailReader(update, failed, fetcher = fetch) {
  let active = true, busy = false, latest = null, controller = null, generation = 0, objectURL = '';
  const clearURL = () => { if (objectURL) URL.revokeObjectURL(objectURL); objectURL = ''; };
  const pump = async () => {
    if (!active || busy || !latest) return;
    const request = latest, ticket = generation; latest = null; busy = true; controller = new AbortController();
    try {
      const response = await fetcher(`/preview/frame/${request.id}?ms=${request.ms}&w=480`, { signal: controller.signal });
      if (!response.ok) throw new Error('frame unavailable');
      const blob = await response.blob(); if (blob.size > 8 * 1024 * 1024) throw new Error('frame too large');
      if (active && ticket === generation && (!latest || latest.id === request.id && latest.ms === request.ms)) { clearURL(); objectURL = URL.createObjectURL(blob); update(objectURL, request.ms); }
    } catch (error) { if (active && ticket === generation && error.name !== 'AbortError') failed(); }
    finally { busy = false; controller = null; if (active && latest) pump(); }
  };
  return {
    request(id, ms) { active = true; latest = { id, ms }; pump(); },
    cancel() { active = false; generation++; latest = null; controller?.abort(); clearURL(); }
  };
}
