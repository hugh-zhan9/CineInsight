import { GetSettings } from '../../wailsjs/go/main/App';

// 移到废纸篓前要不要确认（统一设计第 0 步的集成裁决）：以设置里既有的「删除前确认」（confirm_before_delete，
// 默认开）为准——开着就照旧先确认，关了就直接执行、由删除宿主给「撤销」。不可逆的永久删除不看这个设置，
// 一律走 confirmAction 的 level 'irreversible'。
// 片库页从 App 拿设置；洞察（存储体检）与下载页拿不到，删除时现读一次。读不到时按默认值（开）处理。
export async function shouldConfirmTrash() {
  try {
    const settings = await GetSettings();
    return settings?.confirm_before_delete !== false;
  } catch {
    return true;
  }
}
