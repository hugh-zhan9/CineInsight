// Public error codes remain available to callers; the new panels show explanations.
export function functionalPolishError(error) {
  const message = String(error?.message || error || '');
  const explanations = {
    task_failure_unavailable: '失败记录服务尚未就绪，请稍后再试。',
    task_failure_owner_unavailable: '原任务服务尚未就绪，请稍后再试。',
    task_failure_token_expired: '这次确认已失效或已提交，请重新预览。',
    task_failure_stopped: '任务提交已停止，请等待维护结束后重新预览。',
    task_failure_operation_missing: '这次提交记录已过期，请查看原任务的最新状态。',
    task_failure_empty_selection: '请先选择要重试的失败项。',
    task_failure_selection_mismatch: '所选失败记录已变化，请刷新列表。',
    task_failure_run_mismatch: '诊断时间段已变化，请刷新列表。',
    task_failure_operations_busy: '正在提交其他重试，请等待它完成。',
    task_failure_member_limit: '本次失败超过一百万项，请选择较小的集合。',
    collection_member_missing: '该视频已不在这个作品集中，请刷新。',
  };
  const code = message.match(/\b(task_failure_[a-z_]+|collection_[a-z_]+|anniversary_[a-z_]+)/)?.[0];
  if (explanations[code]) return explanations[code];
  if (code && message.includes(':')) return message.slice(message.indexOf(':') + 1).trim();
  if (code) return '资料已变化或暂时不可用，请刷新后重试。';
  return message || '操作未完成，请稍后重试。';
}
