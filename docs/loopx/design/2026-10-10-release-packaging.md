# 发布打包名称修复

状态：2026-10-10，用户已要求修复名称；本记录先于配置修改。仅改打包输入，不触发发布。

<a id="D-RELEASE-NAME"></a>
**D-RELEASE-NAME · Operational Contract · Accepted**：来源为本轮用户要求及 [AC-03](../../../.loopx/intake/2026-10-10-media-workbench/requirements.md#AC-03)。使用当前 `wails.json` 的输出名“析微影策”定位 macOS `.app`、Windows `.exe`、Linux 可执行文件。路径加引号；下载归档继续使用 `video-master-*`，保持既有归档名称。GitHub Actions 的触发条件和发布权限不变。

所有权在 `.github/workflows/release.yml` 的现有平台矩阵；不引入另一套构建或打包脚本。运行时构建、签名与工具链升级不属于这一项名称修复。

验证：TC-06 对照配置并用临时目录中的中文产物执行可用平台打包；TC-07 缺失输入时检查非零退出。Windows 如无 PowerShell，仅检查命令与配置，不宣称执行通过。基线名称检查已因寻找不到“析微影策.app”失败。远端三平台真实构建与上传未执行。

Support lenses: none。该记录没有新增 API、数据或模块边界，不需要额外架构图。

实施验证（2026-10-10）：三个打包输入已修正。配置名称核对通过；本机执行 macOS zip / Linux tar 临时中文产物夹具并核对归档内容通过，两者缺失输入均非零退出。Windows 无 PowerShell，仅静态核对，远端构建上传未执行。下载归档名保持不变。
