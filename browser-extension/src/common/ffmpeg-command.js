// 「复制 ffmpeg 命令」用的拼装。纯函数。
//
// 这条命令是给用户粘到终端里跑的，所以必须按 POSIX shell 的规矩转义。
// 注意：桌面端桥接那条路不走 shell，Go 侧是直接给 exec 传参数数组的，
// 两条路不共用这段代码——命令行注入面只存在于"人自己粘贴"的这条路上，
// 而这条路上的引号必须是对的，否则带查询串的地址一粘就断。

// 单引号包起来，内部的单引号用 '\'' 的老办法断开再拼。
export function shellQuote(value) {
  const text = String(value ?? '');
  return `'${text.split("'").join(`'\\''`)}'`;
}

export function buildFfmpegCommand({ url, headers = {}, filename = 'output.mp4', copyOnly = true }) {
  const args = ['ffmpeg'];

  const headerLines = [];
  if (headers.referer) headerLines.push(`Referer: ${headers.referer}`);
  if (headers.origin) headerLines.push(`Origin: ${headers.origin}`);
  if (headerLines.length > 0) {
    // ffmpeg 的 -headers 要求整段用 CRLF 分隔并以 CRLF 结尾。
    args.push('-headers', shellQuote(`${headerLines.join('\r\n')}\r\n`));
  }
  if (headers.userAgent) {
    args.push('-user_agent', shellQuote(headers.userAgent));
  }

  args.push('-i', shellQuote(url));
  if (copyOnly) {
    // -c copy 是无损转封装，不重新编码；aac_adtstoasc 是 TS 里的 AAC 进 mp4 必需的。
    args.push('-c', 'copy', '-bsf:a', 'aac_adtstoasc');
  }
  args.push(shellQuote(filename));
  return args.join(' ');
}

// yt-dlp 与 N_m3u8DL-RE 的等价命令，给习惯用它们的人。
export function buildYtDlpCommand({ url, headers = {}, filename = 'output.mp4' }) {
  const args = ['yt-dlp'];
  if (headers.referer) args.push('--referer', shellQuote(headers.referer));
  if (headers.userAgent) args.push('--user-agent', shellQuote(headers.userAgent));
  args.push('-o', shellQuote(filename), shellQuote(url));
  return args.join(' ');
}
