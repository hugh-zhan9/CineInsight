# 本机打包与授权身份

首次执行 `python3 scripts/macos_signing.py setup`，在当前用户默认钥匙串生成并导入专用自签代码签名证书。私钥只允许 `/usr/bin/codesign` 使用；证书指纹和钥匙串路径记录在 `~/Library/Application Support/CineInsight/build-signing.json`。保留钥匙串身份和该配置，不要每次构建重建证书。私钥不进入仓库，脚本不改系统信任或 TCC。

正常使用 `bash scripts/build_and_install_app.sh`，只出包用 `--build-only`。脚本在所有资源和辅助组件装配完毕后，从内向外签名，保持原 Bundle ID；逐项校验辅助程序和最终应用，任何失败阻止安装。超分运行时先核对原始清单，重签后只更新打包副本对应的 files 摘要，models 和 build/enhance-runtime 原产物保持不变。直接 `wails build` 不走这套最终签名。

旧应用采用二进制 cdhash 身份，重编译会改变；新的 designated requirement 绑定 Bundle ID 和证书。第一次从旧包切换到新身份，macOS 可能再要求授权一次；系统权限是否保留须真实升级验证，自签不等于 Developer ID、公证或自动获得权限。不应使用 tccutil reset 处理本问题。

验证：`python3 scripts/test_macos_signing.py` 用两个不同可执行文件的合成应用检查主包和辅助 app 身份一致、资源篡改被拒绝、超分清单损坏被拒绝。真实产物另执行 `codesign --verify --deep --strict 'build/bin/析微影策.app'`。

证书丢失时脚本失败，不自动换证书。同名证书存在但配置丢失时，先核对钥匙串的证书 SHA-1，再恢复上述配置，避免悄悄改变身份。

权威设计：[本机持续签名](../loopx/design/2026-10-30-local-signing/概要设计.md)。依据：[Apple TN2206](https://developer.apple.com/library/archive/technotes/tn2206/_index.html)。
