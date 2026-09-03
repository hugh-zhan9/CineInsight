// 全扩展共用的固定标识。任何一处硬编码字符串在这里出现第二次之前，先加到这里。

// 媒体类型。downloadable=false 的类型只登记与展示，不给内置下载入口。
export const MEDIA_KIND = {
  HLS: 'hls', // m3u8 播放列表（master 或 media 都归这一类，展开后才区分）
  SEGMENT: 'segment', // 单独的 .ts/.m4s 分片，只用来反推它所属的播放列表
  FILE: 'file', // 可直接整取的媒体直链
  DASH: 'dash', // .mpd，只登记不下载
  MSE: 'mse' // 页面用 MediaSource 喂数据，网络层看不到完整流
};

// 后台与界面之间的消息类型。
export const MSG = {
  LIST_DETECTIONS: 'list-detections',
  CLEAR_TAB: 'clear-tab',
  START_DOWNLOAD: 'start-download',
  LIST_TASKS: 'list-tasks',
  PAUSE_TASK: 'pause-task',
  RESUME_TASK: 'resume-task',
  CANCEL_TASK: 'cancel-task',
  REMOVE_TASK: 'remove-task',
  EXPAND_VARIANTS: 'expand-variants',
  PROBE_BRIDGE: 'probe-bridge',
  PUSH_TO_BRIDGE: 'push-to-bridge',
  PLAY_VIA_BRIDGE: 'play-via-bridge',
  SET_CAPTURE: 'set-capture',
  CAPTURE_STATE: 'capture-state',
  MSE_DETECTED: 'mse-detected',
  BLOB_SOURCE: 'blob-source',
  TASKS_CHANGED: 'tasks-changed',
  // offscreen 与 service worker 之间
  OFFSCREEN_READY: 'offscreen-ready',
  OFFSCREEN_START: 'offscreen-start',
  OFFSCREEN_CONTROL: 'offscreen-control',
  OFFSCREEN_PROGRESS: 'offscreen-progress',
  // worker 解析出播放列表之后回报它真正会碰到的主机集合，
  // service worker 据此把请求头规则装全了再放行分片请求。
  OFFSCREEN_HOSTS: 'offscreen-hosts'
};

// 下载任务状态机。terminal 的三个状态之后不会再变。
export const TASK_STATE = {
  QUEUED: 'queued',
  RUNNING: 'running',
  // 转封装：TS 下完之后单独一趟转成 mp4，用户要看得到它在干活。
  REMUXING: 'remuxing',
  PAUSED: 'paused',
  DONE: 'done',
  FAILED: 'failed',
  CANCELED: 'canceled'
};

export const TERMINAL_STATES = [TASK_STATE.DONE, TASK_STATE.FAILED, TASK_STATE.CANCELED];

// CineInsight 桌面端桥接的环回端口区间，与 Go 侧 services.BrowserBridgePortStart/End 同源。
export const BRIDGE_PORT_START = 18110;
export const BRIDGE_PORT_END = 18130;
export const BRIDGE_TOKEN_HEADER = 'X-CineInsight-Token';
export const BRIDGE_PROTOCOL = 1;

// chrome.storage.local 的键。
export const STORAGE_KEYS = {
  SETTINGS: 'settings',
  TASKS: 'tasks',
  BRIDGE_PORT: 'bridgePort',
  CAPTURE_HOSTS: 'captureHosts'
};

// declarativeNetRequest 会话规则的 ID 区间。只有扩展自己发出的请求会命中，
// 见 background/dnr-headers.js 的作用域说明。
export const DNR_RULE_ID_START = 9000;
export const DNR_RULE_ID_END = 9899;
