export function experienceData(result) {
  if (result?.status === 'ok' && result.data !== null && result.data !== undefined && !result.error) return result.data;
  const error = new Error(result?.error?.message || '操作未完成，请刷新后重试');
  error.code = result?.error?.code || 'invalid_response';
  throw error;
}
