import { GetSettings } from '../../wailsjs/go/main/App';

// 移到废纸篓前要不要确认（统一设计第 0 步的集成裁决）：以设置里既有的「删除前确认」（confirm_before_delete，
// 默认开）为准——开着就照旧先确认，关了就直接执行、由删除宿主给「撤销」。不可逆的永久删除不看这个设置，
// 一律走 confirmAction 的 level 'irreversible'。
// 片库页从 App 拿设置；洞察（存储体检）、下载页与人物页的删除对话框拿不到，删除时现读一次。读不到时按默认值（开）处理。
export async function shouldConfirmTrash() {
  return (await readDeletePreferences()).confirmFirst;
}

// 同一次读取顺带给出「删除原文件」（delete_original_file，默认关）：不确认直接删时按它决定移到废纸篓还是只删记录。
// 读不到时两项都按默认值：先确认、不删原文件。
export async function readDeletePreferences() {
  try {
    const settings = await GetSettings();
    return {
      confirmFirst: settings?.confirm_before_delete !== false,
      deleteOriginal: Boolean(settings?.delete_original_file)
    };
  } catch {
    return { confirmFirst: true, deleteOriginal: false };
  }
}
