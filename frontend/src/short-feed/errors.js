// 手机端失败提示（D-PC45、D-PC46、PLAY-13）：按服务端的机器码给固定中文，认不出的码用服务端的 message。
// 这里只放纯函数，node 脚本测试可以直接调用。

export const PIN_MIN_LENGTH = 6;

// PIN 登录的每日上限（24 小时内全局失败过多）：只能在电脑上解除，或等 24 小时。
export const DAILY_LOCKED_TEXT = '请在电脑上解除锁定，或 24 小时后再试';

const CODE_TEXT = {
  pin_required: '需要输入 PIN',
  pin_invalid: 'PIN 不正确',
  auth_unavailable: '暂时无法校验访问权限，请稍后再试',
  // 删除时的三种 409：什么都没动，回桌面端处理或接上磁盘再试。
  volume_offline: '文件所在磁盘当前未连接，未做任何改动',
  permission_denied: '没有权限把文件移到废纸篓，请在桌面端处理',
  trash_unsupported: '该磁盘不支持废纸篓，请在桌面端处理'
};

// feedErrorText 把一次失败翻成给人看的一句话。
export function feedErrorText(err) {
  if (!err) return '';
  if (err.network) return '网络异常';
  const code = String(err.code || '');
  if (code === 'pin_locked') {
    if (err.payload?.daily_locked) return DAILY_LOCKED_TEXT;
    const seconds = Number(err.payload?.retry_after);
    return Number.isFinite(seconds) && seconds > 0 ? `尝试次数过多，请 ${Math.ceil(seconds)} 秒后再试` : '尝试次数过多，请稍后再试';
  }
  if (CODE_TEXT[code]) return CODE_TEXT[code];
  return String(err.message || err);
}

// PIN 输入的本地检查：6 位以下不发请求，免得白白消耗失败次数（服务端设置 PIN 时同样要求至少 6 位）。
export function pinInputProblem(pin) {
  const length = [...String(pin ?? '')].length;
  if (length === 0) return '请输入 PIN';
  if (length < PIN_MIN_LENGTH) return `PIN 至少 ${PIN_MIN_LENGTH} 位`;
  return '';
}

// 范围面板里「因格式放不了」的一段说明（D-PC46、PLAY-13；D-MW-07 改成设计稿的白话）：0 条时不显示。
export function unplayableHintText(count) {
  const value = Number(count);
  if (!Number.isFinite(value) || value <= 0) return '';
  return `另有 ${value} 个视频手机放不了（格式不支持）。在电脑端片库里为它们生成兼容副本后就会出现在这里。`;
}

const MEDIA_KIND_WORDS = { video: '视频', image: '图片' };

// 空范围的原因（D-MW-07）：有被格式门排除的视频就写明条数与出路，否则只说这个范围里现在没东西。
export function emptyScopeText(scopeName, mediaKind, unplayableCount) {
  const name = `「${scopeName || '全部'}」`;
  const count = Number(unplayableCount);
  if (Number.isFinite(count) && count > 0) {
    return `${name}里的 ${count} 个视频手机都放不了。在电脑端为它们生成兼容副本，或者换个范围。`;
  }
  return `${name}里现在没有${MEDIA_KIND_WORDS[mediaKind] || '视频或图片'}，换个范围试试。`;
}
