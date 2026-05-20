# AI 智能增强清单

## 目标
借鉴 Immich 的机器学习架构，但在当前项目中优先落地低资源、可回退、无需重模型下载的智能能力。

分析基线：
- Immich 仓库：`immich-app/immich`
- 分析提交：`20da7c4`
- 分析范围：`machine-learning/`、`server/src/repositories/machine-learning.repository.ts`、`server/src/services/person.service.ts`、`server/src/services/smart-info.service.ts`、`server/src/services/queue.service.ts`

## 现状对照
- 已有能力：远程 OpenAI 兼容的 AI 打标、字幕文本证据、抽帧证据、候选审核、状态跟踪。
- 缺口：本地优先分析、轻量人脸能力、跨视频稳定识别、统一分析报告、低配设备友好策略。

## Immich 可借鉴点
- 独立机器学习入口：FastAPI `/predict`
- 任务类型：CLIP、facial-recognition、OCR
- 模型缓存：内存缓存 + TTL
- 健康检查：服务 URL 轮询和失败降级
- 队列隔离：SmartSearch、FaceDetection、FacialRecognition 分队列
- 结果落库：face、person、smart_search、ocr 独立表
- 人工管理：person 可命名、合并、隐藏

## 计划清单
1. 本地优先的视频智能打标：已实现基础规则 fallback。
2. 轻量人脸检测：已接入纯 Go Pigo 检测器和 234KB `facefinder` 级联资源。
3. 轻量人脸识别/聚类：已建立 `video_faces` 与 `face_clusters` 表，按签名聚类。
4. 低成本视频特征提取：已沿用现有字幕、抽帧、元数据证据链。
5. 统一分析状态与候选审查：沿用 AI 标签候选审核，不直接写正式标签。
6. 轻量自然语言搜索：已新增智能搜索入口，支持人脸、标签、字幕、分辨率、体积和横竖屏等常见口语条件。
7. 远程模型作为可选增强：远程失败或缺配置时回退本地分析。

## 非目标
- 不引入必须下载的大型模型
- 不引入 GPU 强依赖
- 不引入常驻重服务
- 不牺牲现有标签审核链路

## 交付顺序
1. 规则型本地分析客户端
2. 轻量人脸检测资源接入
3. 人脸签名与聚类
4. 后端 API 暴露人脸分析
5. 前端列表页暴露智能搜索和人脸分析入口
6. 前端审核页补充分析摘要
7. 再评估是否需要额外的远程增强接口
