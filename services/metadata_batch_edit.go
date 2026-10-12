package services

// 骨架：数据维护（docs/loopx/design/2026-10-12-data-care）。实现按详细设计补齐。

// MetadataCompletenessService 是元数据完整度统计（D-MC-02）。
type MetadataCompletenessService struct {
	registry *BackgroundTaskRegistry
}

func NewMetadataCompletenessService(dataDir string) *MetadataCompletenessService {
	return &MetadataCompletenessService{}
}

func (s *MetadataCompletenessService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.registry = registry
	}
}

// StopAndWait 停止并等待进行中的工作（进入恢复维护与退出时调用）。
func (s *MetadataCompletenessService) StopAndWait() {}

// MetadataBatchEditService 是元数据批量编辑与撤销（D-MC-04 / D-MC-05）。
type MetadataBatchEditService struct {
	registry *BackgroundTaskRegistry
}

func NewMetadataBatchEditService(dataDir string) *MetadataBatchEditService {
	return &MetadataBatchEditService{}
}

func (s *MetadataBatchEditService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.registry = registry
	}
}

// StopAndWait 停止并等待进行中的工作（进入恢复维护与退出时调用）。
func (s *MetadataBatchEditService) StopAndWait() {}
