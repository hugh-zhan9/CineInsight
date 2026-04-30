export function confidenceMeta(confidence) {
  const value = String(confidence || '').toLowerCase();
  if (value === 'high') {
    return {
      label: '高置信',
      className: 'ai-confidence--high',
      rank: 0,
    };
  }
  if (value === 'medium') {
    return {
      label: '中置信',
      className: 'ai-confidence--medium',
      rank: 1,
    };
  }
  return {
    label: '未知',
    className: 'ai-confidence--unknown',
    rank: 2,
  };
}

export function groupCandidatesByVideo(candidates) {
  const groupsByKey = new Map();
  for (const candidate of Array.isArray(candidates) ? candidates : []) {
    const video = candidate?.video || {};
    const key = String(candidate?.video_id || video.id || 'unknown');
    if (!groupsByKey.has(key)) {
      groupsByKey.set(key, {
        videoId: candidate?.video_id || video.id || 0,
        videoName: video.name || `视频 #${candidate?.video_id || ''}`,
        videoPath: video.path || '',
        candidates: [],
      });
    }
    groupsByKey.get(key).candidates.push(candidate);
  }
  return Array.from(groupsByKey.values()).map(group => ({
    ...group,
    candidates: group.candidates.slice().sort((a, b) => confidenceMeta(a.confidence).rank - confidenceMeta(b.confidence).rank),
  }));
}

export function removeCandidateById(candidates, candidateId) {
  const id = Number(candidateId);
  return (Array.isArray(candidates) ? candidates : []).filter(candidate => Number(candidate.id) !== id);
}

