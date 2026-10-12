package services

// 骨架：数据维护（docs/loopx/design/2026-10-12-data-care）。实现按详细设计补齐。

// LostMediaService 是失联数据治理（D-LD-*）：失联报告、找回、清除与孤儿文件回收。
type LostMediaService struct {
	registry *BackgroundTaskRegistry
}

func NewLostMediaService(dataDir string) *LostMediaService {
	return &LostMediaService{}
}

func (s *LostMediaService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.registry = registry
	}
}

// StopAndWait 停止并等待进行中的工作（进入恢复维护与退出时调用）。
func (s *LostMediaService) StopAndWait() {}
