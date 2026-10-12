package services

// 骨架：数据维护（docs/loopx/design/2026-10-12-data-care）。实现按详细设计补齐。

// DataHealthService 是数据体检（D-DH-04 / D-DH-05）：只读检查与用户点击的修复。
type DataHealthService struct {
	registry *BackgroundTaskRegistry
}

func NewDataHealthService(dataDir string) *DataHealthService {
	return &DataHealthService{}
}

func (s *DataHealthService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.registry = registry
	}
}

// StopAndWait 停止并等待进行中的工作（进入恢复维护与退出时调用）。
func (s *DataHealthService) StopAndWait() {}
