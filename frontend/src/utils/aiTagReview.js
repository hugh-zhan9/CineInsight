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
        video,
        videoTags: Array.isArray(video.tags) ? video.tags : [],
        videoDeleted: Boolean(candidate?.video_deleted),
        candidates: [],
      });
    }
    const group = groupsByKey.get(key);
    group.videoDeleted = group.videoDeleted || Boolean(candidate?.video_deleted);
    group.candidates.push(candidate);
  }
  return Array.from(groupsByKey.values()).map(group => ({
    ...group,
    candidates: group.candidates.slice().sort((a, b) => confidenceMeta(a.confidence).rank - confidenceMeta(b.confidence).rank),
  }));
}

export function filterCandidatesForReview(candidates, searchTerm, mediaField = 'video') {
  const keyword = String(searchTerm || '').trim().toLowerCase();
  const list = Array.isArray(candidates) ? candidates : [];
  if (!keyword) return list;
  return list.filter(candidate => {
    const video = candidate?.[mediaField] || {};
    return [
      video.name,
      video.path,
      ...(Array.isArray(video.tags) ? video.tags.map(tag => tag?.name) : []),
      candidate?.suggested_name,
      candidate?.matched_tag?.name,
      candidate?.reasoning,
    ].some(value => String(value || '').toLowerCase().includes(keyword));
  });
}

export function removeCandidateById(candidates, candidateId) {
  const id = Number(candidateId);
  return (Array.isArray(candidates) ? candidates : []).filter(candidate => Number(candidate.id) !== id);
}

export function createRejectVideoConfirm(group) {
  if (!group?.videoId) return null;
  const candidates = Array.isArray(group.candidates) ? group.candidates : [];
  if (candidates.length === 0) return null;
  return {
    show: true,
    videoId: group.videoId,
    videoName: group.videoName || `视频 #${group.videoId}`,
    count: candidates.length,
    candidateIds: candidates.map(candidate => Number(candidate.id)),
  };
}

// 审批只联动同一标签 ID；历史名称归一化相同的不同标签必须各自保留。
function candidateTagKey(candidate) {
  return Number(candidate?.matched_tag_id) || null;
}

// 翻页追加：后端按 id DESC 发页，游标是上一页最后一条的 id。重复只可能来自
// 两次请求之间的并发写入，按 id 去重——同一条候选出现两次会让 Vue 的 :key 也撞上。
export function appendCandidates(existing, incoming) {
  const merged = Array.isArray(existing) ? [...existing] : [];
  const seen = new Set(merged.map(candidate => Number(candidate?.id)));
  for (const candidate of Array.isArray(incoming) ? incoming : []) {
    const id = Number(candidate?.id);
    if (seen.has(id)) continue;
    seen.add(id);
    merged.push(candidate);
  }
  return merged;
}

// 整媒体移除：拒绝某个视频/图片的全部待审候选，或重新分析把它们置 superseded 之后用。
export function removeCandidatesByMedia(candidates, mediaField, mediaId) {
  const id = Number(mediaId);
  return (Array.isArray(candidates) ? candidates : []).filter(candidate => Number(candidate?.[mediaField]) !== id);
}

// 接受一条候选后后端还会动别的行：同媒体同标签的其他待审候选置 superseded。
// 前端按同一条规则局部移除，而不是整表重拉——分页之后重拉会把已加载的页全丢掉。
// D-PC28 规则 2 之后，媒体上已有手工标签不再让整媒体的候选作废（META-06），
// 这里也不再有「整媒体移除」的分支：同媒体其他标签的候选一律留在列表里。
// approvedItem 保留在签名里只为调用方不变，结果不再依赖它的 status。
export function removeCandidatesAfterApproval(candidates, candidate, approvedItem, mediaField = 'video_id') {
  const list = Array.isArray(candidates) ? candidates : [];
  const mediaId = Number(candidate?.[mediaField]);
  const approvedKey = candidateTagKey(candidate);
  return list.filter(item => {
    if (Number(item?.id) === Number(candidate?.id)) return false;
    if (Number(item?.[mediaField]) !== mediaId) return true;
    return approvedKey === null || candidateTagKey(item) !== approvedKey;
  });
}

// 手动给视频加标签之后，后端把同视频、matched_tag_id 等于该标签的待审候选置 superseded
// （原因「已手动添加」，D-PC28 规则 2）。AddTagToVideo 不回传被作废的 ID，前端按
// (video_id, tag_id) 同一口径局部移除；同视频其他标签的候选保留。
export function removeCandidatesForManualTags(candidates, videoId, tagIds) {
  const id = Number(videoId);
  const tags = new Set((Array.isArray(tagIds) ? tagIds : []).map(Number).filter(Boolean));
  const list = Array.isArray(candidates) ? candidates : [];
  if (!id || tags.size === 0) return list;
  return list.filter(candidate => Number(candidate?.video_id) !== id || !tags.has(candidateTagKey(candidate)));
}

// 只有高、中置信且视频未删除的候选能被批准（后端 ApproveCandidate 的同一口径），
// 批量批准只把这些 ID 交出去，避免一批结果里塞满必然失败的项。
export function isApprovableCandidate(candidate) {
  const confidence = String(candidate?.confidence || '').toLowerCase();
  return !candidate?.video_deleted && (confidence === 'high' || confidence === 'medium');
}

export function approvableCandidateIDs(candidates) {
  return (Array.isArray(candidates) ? candidates : [])
    .filter(isApprovableCandidate)
    .map(candidate => Number(candidate.id))
    .filter(Boolean);
}

// 审阅列表的结构化筛选：置信度精确匹配、候选标签按 matched_tag_id 精确匹配。
// 关键词检索另走 filterCandidatesForReview，两者叠加就是「当前筛选结果」。
export function filterCandidatesByAttributes(candidates, { confidence = '', tagId = 0 } = {}) {
  const wantedConfidence = String(confidence || '').toLowerCase();
  const wantedTag = Number(tagId) || 0;
  return (Array.isArray(candidates) ? candidates : []).filter(candidate => {
    if (wantedConfidence && String(candidate?.confidence || '').toLowerCase() !== wantedConfidence) return false;
    if (wantedTag && candidateTagKey(candidate) !== wantedTag) return false;
    return true;
  });
}

// 已加载候选里出现过的标签（按名称排序），供筛选栏的标签下拉使用。
export function candidateTagOptions(candidates) {
  const byID = new Map();
  for (const candidate of Array.isArray(candidates) ? candidates : []) {
    const id = candidateTagKey(candidate);
    if (!id || byID.has(id)) continue;
    byID.set(id, { id, name: candidate?.matched_tag?.name || candidate?.suggested_name || `标签 #${id}` });
  }
  return [...byID.values()].sort((a, b) => String(a.name).localeCompare(String(b.name), 'zh-Hans-CN'));
}

function tallyMessages(items, fallback) {
  const counts = new Map();
  for (const item of items) {
    const message = String(item?.message || '').trim() || fallback;
    counts.set(message, (counts.get(message) || 0) + 1);
  }
  return [...counts.entries()].map(([message, count]) => ({ message, count }));
}

// 批量批准（META-11）的结果落到已加载列表上：成功项按单条批准的同一规则局部移除
// （连同同视频同标签的其余候选）；superseded 项已不在待审，直接移除；真正失败的项留着，
// 让用户看得见、能单独重试。返回新列表与按原因归并的摘要。
export function applyBatchApprovalResult(candidates, result) {
  let list = Array.isArray(candidates) ? candidates : [];
  const items = Array.isArray(result?.results) ? result.results : [];
  const byID = new Map(list.map(candidate => [Number(candidate?.id), candidate]));
  const superseded = [];
  const failed = [];
  for (const item of items) {
    const id = Number(item?.id);
    if (item?.ok) {
      const approved = byID.get(id) || item.item || { id };
      list = removeCandidatesAfterApproval(list, approved, item.item, 'video_id');
    } else if (item?.superseded) {
      superseded.push(item);
      list = removeCandidateById(list, id);
    } else {
      failed.push(item);
    }
  }
  return {
    candidates: list,
    summary: {
      requested: Number(result?.requested ?? items.length) || 0,
      succeeded: Number(result?.succeeded ?? items.filter(item => item?.ok).length) || 0,
      superseded: Number(result?.superseded ?? superseded.length) || 0,
      failed: Number(result?.failed ?? failed.length) || 0,
      supersededReasons: tallyMessages(superseded, '候选已失效'),
      failureReasons: tallyMessages(failed, '批准失败')
    }
  };
}

// 批量结果的一句话说明。superseded 不是失败：同标签的另一条已批准、已手动添加等，
// 原因按后端逐项文案归并后列出来（META-11），否则用户分不清「没批上」和「不用批」。
export function batchApprovalSummaryText(summary) {
  if (!summary) return '';
  const parts = [`批量批准完成：成功 ${summary.succeeded} 条`];
  if (summary.superseded) {
    const reasons = (summary.supersededReasons || []).map(reason => `${reason.message} ${reason.count} 条`).join('；');
    parts.push(`${summary.superseded} 条已失效（${reasons}）`);
  }
  if (summary.failed) {
    const reasons = (summary.failureReasons || []).map(reason => `${reason.message} ${reason.count} 条`).join('；');
    parts.push(`${summary.failed} 条失败（${reasons}）`);
  }
  return `${parts.join('，')}。`;
}

// Outcomes only identify rows to recheck. Old batches cannot decide whether a
// newly loaded or newly analysed candidate is still pending now.
export function reviewOutcomeCandidateIDs(candidates, outcomes, mediaField = 'video_id') {
  const ids = new Set((outcomes || []).map(item => Number(item.id)));
  const targets = new Set((outcomes || []).filter(item => item.state === 'approved' && item.media_id && item.tag_id).map(item => `${Number(item.media_id)}:${Number(item.tag_id)}`));
  return (candidates || []).filter(item => ids.has(Number(item.id)) || targets.has(`${Number(item[mediaField])}:${Number(item.matched_tag_id)}`)).map(item => Number(item.id));
}

export function applyReviewCandidateRefresh(candidates, snapshot, pending) {
  const expected = new Map(snapshot.map(item => [Number(item.id), item]));
  const current = new Map(pending.map(item => [Number(item.id), item]));
  return candidates.flatMap(item => {
    const id = Number(item.id);
    if (expected.get(id) !== item) return [item];
    return current.has(id) ? [current.get(id)] : [];
  });
}
