// Keep the command registry independent of the async Settings page component.
export const SETTINGS_CATEGORIES = [
  { key: 'general', label: '通用与外观' },
  { key: 'library', label: '片库与扫描' },
  { key: 'playback', label: '播放与处理' },
  { key: 'tasks', label: '自动化与任务' },
  { key: 'ai', label: 'AI 与字幕' },
  { key: 'connections', label: '移动端与外部连接' },
  { key: 'data', label: '数据与备份' }
];

export const SETTINGS_SECTIONS = [
  { key: 'basic', category: 'general', label: '基本设置', saveMode: 'save' },
  { key: 'scan-dirs', category: 'library', label: '扫描目录管理', saveMode: 'instant' },
  { key: 'image-dirs', category: 'library', label: '图片扫描目录', saveMode: 'instant' },
  { key: 'video-formats', category: 'library', label: '支持的视频格式', saveMode: 'save' },
  { key: 'image-formats', category: 'library', label: '支持的图片格式', saveMode: 'save' },
  { key: 'iina-sync', category: 'playback', label: 'IINA 进度同步', saveMode: 'instant' },
  { key: 'playback-proxy', category: 'playback', label: '播放兼容缓存', saveMode: 'save' },
  { key: 'enhance', category: 'playback', label: '视频超分', saveMode: 'instant' },
  { key: 'random', category: 'playback', label: '智能随机播放', saveMode: 'save' },
  { key: 'automation', category: 'tasks', label: '自动化与扫描', saveMode: 'save' },
  { key: 'idle-scheduling', category: 'tasks', label: '后台任务调度', saveMode: 'save' },
  { key: 'automatic-structure', category: 'tasks', label: '自动分段', saveMode: 'save' },
  { key: 'ai-tags', category: 'ai', label: 'AI 标签', saveMode: 'save' },
  { key: 'face', category: 'ai', label: '人脸识别', saveMode: 'save' },
  { key: 'scenes', category: 'ai', label: '场景检索', saveMode: 'save' },
  { key: 'subtitle-translate', category: 'ai', label: '字幕翻译', saveMode: 'save' },
  { key: 'subtitle-quality', category: 'ai', label: '字幕识别质量', saveMode: 'save' },
  { key: 'mobile', category: 'connections', label: '手机端浏览', saveMode: 'mixed' },
  { key: 'jellyfin', category: 'connections', label: 'Jellyfin 客户端连接', saveMode: 'instant' },
  { key: 'bt', category: 'connections', label: 'Prowlarr 与 qBittorrent', saveMode: 'instant' },
  { key: 'browser-bridge', category: 'connections', label: '浏览器插件', saveMode: 'save' },
  { key: 'online-sources', category: 'connections', label: '在线资料源', saveMode: 'save' },
  { key: 'photo-map', category: 'connections', label: '照片地图', saveMode: 'instant' },
  { key: 'database', category: 'data', label: '数据库', saveMode: 'instant' },
  { key: 'backup', category: 'data', label: '数据库备份', saveMode: 'save' }
];

export function settingsSection(key) {
  return SETTINGS_SECTIONS.find(section => section.key === key) || null;
}
