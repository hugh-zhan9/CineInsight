import { GetBTConnection, ConfigureBTConnection, TestBTConnection, ListBTIndexers, SearchBTResources, PreviewBTResource, SubmitBTDownload, ListBTTasks, GetBTTask, ControlBTTask } from '../../wailsjs/go/main/App';

// Only this adapter knows the Wails list shape. All other DTOs remain unchanged.
export const btAPI = {
  GetBTConnection: (...args) => GetBTConnection(...args),
  ConfigureBTConnection: (...args) => ConfigureBTConnection(...args),
  TestBTConnection: (...args) => TestBTConnection(...args),
  ListBTIndexers: (...args) => ListBTIndexers(...args),
  SearchBTResources: (...args) => SearchBTResources(...args),
  PreviewBTResource: (...args) => PreviewBTResource(...args),
  SubmitBTDownload: (...args) => SubmitBTDownload(...args),
  GetBTTask: (...args) => GetBTTask(...args),
  ControlBTTask: (...args) => ControlBTTask(...args),
  async ListBTTasks(beforeID, limit) {
    const tasks = await ListBTTasks(beforeID, limit);
    return { tasks, next_cursor: tasks.length === limit ? tasks[tasks.length - 1].id : '' };
  }
};
export function btConnectionError(error) {
  const code = typeof error?.code === 'string' ? error.code : String(error?.message || error || '').split(':', 1)[0];
  return ({ bt_restore_pending: '下载任务的恢复核实尚未完成，请稍后重试；如持续未完成，请重新打开应用。', bt_auth_failed: '认证失败，请检查已保存的账号和密钥。', bt_service_unavailable: '服务暂时不可用，请确认本机服务已启动。', bt_endpoint_invalid: '请填写本机服务的 HTTP 或 HTTPS 地址。', bt_response_invalid: '服务返回的数据无法识别，请检查连接和版本。', bt_response_too_large: '服务返回的数据超过处理上限。', bt_connection_unavailable: '请先配置并启用下载连接。', bt_connection_active: '仍有受管理任务，暂不能更换服务地址或下载目录。', bt_credentials_busy: '凭据正在被另一操作使用，请稍后重试。', bt_credentials_unavailable: '系统钥匙串不可用，请检查系统权限。', auth_failed: '认证失败，请检查已保存的账号和密钥。', service_unavailable: '服务暂时不可用，请确认本机服务已启动。', connection_required: '请先保存连接配置。', bt_conflict: '连接配置已变化，请刷新后重试。', credentials_busy: '凭据正在被另一操作使用，请稍后重试。', credentials_unavailable: '系统钥匙串不可用，请检查系统权限。', version_unsupported: '下载器版本尚未通过兼容性验证。', path_not_in_library: '下载位置不在片库扫描范围内，请先配置扫描目录。', maintenance: '片库维护中，请稍后再试。' }[code] || '操作未完成，请检查连接设置后重试。');
}
