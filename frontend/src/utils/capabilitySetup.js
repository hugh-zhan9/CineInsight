export const SETUP_GOALS = [
  { key: 'watch', label: '我要看片' }, { key: 'organize', label: '整理片库' },
  { key: 'scenes', label: '搜索画面' }, { key: 'subtitles', label: '制作字幕' }
];
export const SETUP_STATE_LABELS = { ready: '可用', needs_action: '需要准备', preparing: '正在处理', unsupported: '当前平台不支持', unknown: '尚未检查', failed: '检查失败' };
export function setupState(item) {
  if (!item) return 'unknown';
  if (item.reason_code === 'unsupported_platform') return 'unsupported';
  if (['preparing', 'running', 'syncing', 'waiting_idle'].includes(item.reason_code)) return 'preparing';
  if (['check_failed', 'last_run_failed'].includes(item.reason_code)) return 'failed';
  if (item.state === 'unknown') return 'unknown';
  return item.state === 'ok' ? 'ready' : 'needs_action';
}
export function capabilityGoalSteps(goal, snapshots, facts = {}) {
  const from = (section, key, label, command, actionLabel = '打开对应设置') => {
    const item = snapshots[section]?.items?.find(item => item.key === key);
    return { key, label, state: setupState(item), detail: item?.detail || '尚未获得检查结果', checkedAt: item?.observed_at || snapshots[section]?.checked_at, metrics: item?.metrics || [], command: item?.action_id || command, actionLabel };
  };
  const roots = snapshots.storage?.items?.filter(item => /^root_\d+$/.test(item.key)) || [];
  const directory = from('storage', 'roots', '视频扫描目录', 'action:settings:scan-dirs', '选择或检查目录');
  if (roots.length) {
    directory.state = roots.some(item => ['unavailable', 'warning'].includes(item.state)) ? 'needs_action' : 'ready';
    directory.detail = `已添加 ${roots.length} 个目录；可到设置检查磁盘与扫描范围`;
  }
  const library = { key: 'library', label: '片库内容', state: Number.isFinite(facts.count) ? facts.count > 0 ? 'ready' : 'needs_action' : 'unknown', detail: Number.isFinite(facts.count) ? `当前可见片库有 ${facts.count} 部视频` : '尚未读到片库数量', command: facts.count > 0 ? 'nav:page:videos' : 'action:scan-new', actionLabel: facts.count > 0 ? '打开片库' : '扫描新文件' };
  if (goal === 'watch') return [directory, library, from('runtimes', 'ffmpeg', '音视频处理工具', 'action:settings:subtitle-quality')];
  if (goal === 'organize') return [library, { key: 'pending', label: '待审阅事项', state: Number.isFinite(facts.pending) ? facts.pending > 0 ? 'needs_action' : 'ready' : 'unknown', detail: Number.isFinite(facts.pending) ? `${facts.pending} 项待处理；技术信息与元数据可在片库管理中补全` : '尚未读到待处理数量', command: 'action:pending-work', actionLabel: '打开待处理' }, { key: 'metadata', label: '技术信息与元数据', state: 'unknown', detail: '在片库管理中选择需要补全的内容，按明确范围开始处理', command: 'nav:page:library-manage', actionLabel: '打开片库管理' }];
  if (goal === 'scenes') return [from('runtimes', 'scene', '画面模型', 'action:settings:scenes', '准备或检查模型'), from('indexes', 'scene_visual', '画面索引', 'nav:page:scenes', '选择范围建立索引')];
  const speech = from('runtimes', 'apple', '系统识别与语言资源', 'action:settings:subtitle-quality', '准备或检查语言');
  if (speech.state === 'ready' && speech.metrics.some(metric => metric.key === 'speech_languages_ready' && metric.value === 0)) { speech.state = 'needs_action'; speech.detail = '系统识别可用，还需要准备识别语言'; }
  return [from('runtimes', 'ffmpeg', '音频提取工具', 'action:settings:subtitle-quality'), speech, { key: 'translate', label: '双语翻译（可选）', state: 'unknown', detail: '在字幕设置中选择语言对并检查系统资源；普通字幕不要求翻译', command: 'action:settings:subtitle-translate', actionLabel: '选择翻译语言' }, { key: 'choose', label: '选择视频生成字幕', state: library.state, detail: '在片库行菜单选择“生成字幕”，语言与覆盖范围由你确认', command: 'nav:page:library-manage', actionLabel: '选择视频' }];
}
