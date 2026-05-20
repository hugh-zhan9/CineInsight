# AI 智能增强测试计划

## 单元测试
- 本地配置缺失时可继续运行：`TestAITaggingLocalFallbackCreatesCandidatesWhenRemoteConfigMissing`
- 远程分析失败时可回退本地分析：`TestAITaggingFallsBackToLocalCandidatesWhenRemoteAnalyzeFails`
- 本地分析会输出确定性候选：同上
- 候选不会绕过人工审批直接写正式标签：沿用 `TestAITaggingPersistsCandidateButDoesNotWriteOfficialTablesBeforeApproval`
- 人脸签名聚类规则稳定：`TestVideoFaceServicePersistsDetectedFacesAndClusters`
- 检测器不可用时降级：`TestVideoFaceServiceSkipsWhenDetectorUnavailable`
- 人脸证据参与打标：`TestLocalAITaggingUsesPersistedFaceEvidence`
- 自然语言搜索可映射到轻量索引：`TestSearchVideosWithFiltersUnderstandsNaturalLanguageHints`

## 集成测试
- 设置页保存后配置可被服务读取
- 视频分析服务能处理无字幕、无抽帧、无脸资源三种退化场景
- 审核页相关 API 返回的候选和摘要一致
- Wails 绑定暴露 `AnalyzeVideoFaces` 和 `SearchVideosSmart`：`frontend/scripts/ai-ml-bindings.test.mjs`
- 视频列表暴露“智能搜索”和“人脸”入口：`frontend/scripts/video-list-ui.test.mjs`

## 回归测试
- 现有字幕、标签、扫描、迁移、分页测试继续通过
- 现有 AI 标签审批流程继续通过

## 验证门槛
- `go test ./...`
- 前端构建至少通过一次
- 不依赖模型下载的默认路径可启动
