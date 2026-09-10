export function deletionActor(source) {
  return ({ user: '用户', scanner: '程序（扫描）' })[source] || '历史未知';
}
