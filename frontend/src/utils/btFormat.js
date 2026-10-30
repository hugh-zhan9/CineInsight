// Byte totals stay exact across JSON boundaries; unsafe JS numbers are rejected.
export function btBytes(value) {
  if (typeof value === 'bigint') return value >= 0n ? value : null;
  if (typeof value === 'number') return Number.isSafeInteger(value) && value >= 0 ? BigInt(value) : null;
  if (typeof value === 'string' && /^\d+$/.test(value)) return BigInt(value);
  return null;
}
export function btByteText(value) {
  const n = btBytes(value);
  return n === null ? '大小未知' : `${n.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',')} 字节`;
}
export function btSelectedBytes(files, ids) {
  const selected = new Set(ids.map(String));
  let total = 0n;
  for (const file of files) {
    if (!selected.has(String(file.id))) continue;
    const n = btBytes(file.size);
    if (n === null) return null;
    total += n;
  }
  return total.toString();
}
export const btTaskStatus = value => ({ stoppedDL: '已停止', stoppedUP: '已停止做种', pausedDL: '已暂停', pausedUP: '已暂停做种', uploading: '做种中', stalledUP: '等待做种连接', stalledDL: '等待下载连接', queuedDL: '排队下载', queuedUP: '排队做种', checkingDL: '校验中', checkingUP: '校验中', checkingResumeData: '核对恢复数据', metaDL: '获取文件清单', missingFiles: '文件缺失', error: '下载器错误', draft: '待选择', metadata_adding: '准备元数据', metadata_wait: '等待文件清单', metadata_ready: '文件清单就绪', adding: '正在提交', selecting: '正在应用文件选择', starting: '正在启动', active: '已交给下载器', uncertain: '操作结果待核实', conflict: '任务已发生变化', missing: '下载器中未找到任务', abandoned: '已放弃提交', cancelled: '已取消', cancel_pending: '正在取消', completed: '下载完成', downloading: '下载中', seeding: '做种中', stopped: '已停止', checking: '校验中', moving: '移动中', failed: '失败' }[value] || '状态待核实');
export const btImportStatus = value => ({ pending: '等待入库', importing: '正在入库', imported: '已入库', failed: '入库失败', blocked_user_delete: '已从片库移除', withheld: '等待下载完成' }[value] || '等待核实');

// error_message is the backend's sanitized user-facing DTO field. Never fall
// through to arbitrary error_code strings or raw upstream response fields.
export function btFileErrorText(file) {
  if (typeof file.error_message === 'string' && file.error_message.trim()) return file.error_message.trim();
  return ({ import_failed: '文件入库失败，请检查后重新入库。', user_deleted: '该文件已从片库移除，不会自动重新收录。', blocked_user_delete: '该文件已从片库移除，不会自动重新收录。', path_unsafe: '文件路径不符合保存范围，无法入库。', path_not_in_library: '下载位置尚未加入片库扫描范围。', download_missing: '未找到下载文件，请检查下载器。', external_changed: '文件或下载设置已变化，请重新核实。', service_unavailable: '下载服务暂时不可用，请稍后核实。', maintenance: '片库维护中，请稍后重试。' }[file.error_code] || '此文件处理失败，请重新核实任务状态。');
}
