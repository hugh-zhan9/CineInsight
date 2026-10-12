package services

// 骨架：数据维护（docs/loopx/design/2026-10-12-data-care）。实现按详细设计补齐。

// AutomationRuleService 是本地规则自动化（D-RA-*）：规则存取、新入库队列与手动运行。
type AutomationRuleService struct {
	registry *BackgroundTaskRegistry
}

func NewAutomationRuleService(dataDir string) *AutomationRuleService {
	return &AutomationRuleService{}
}

func (s *AutomationRuleService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.registry = registry
	}
}

// StopAndWait 停止并等待进行中的工作（进入恢复维护与退出时调用）。
func (s *AutomationRuleService) StopAndWait() {}
