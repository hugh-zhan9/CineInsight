package services

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 保存设置时把空闲调度的入参收进合法区间：存进去的就是生效值，
// 空闲门读设置时不用再猜一遍。
func TestUpdateSettingsNormalizesIdleScheduling(t *testing.T) {
	database.DB = dbtest.Open(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	service := &SettingsService{}

	cases := []struct {
		name           string
		input          models.Settings
		wantEnabled    bool
		wantThreshold  int
		wantRequireAC  bool
		wantWindowFrom string
		wantWindowTo   string
	}{
		{
			name: "越界阈值收进 1–120",
			input: models.Settings{
				IdleSchedulingEnabled: true, IdleThresholdMinutes: 999,
				IdleWindowStart: "22:00", IdleWindowEnd: "06:00",
			},
			wantEnabled: true, wantThreshold: 120, wantWindowFrom: "22:00", wantWindowTo: "06:00",
		},
		{
			name:        "非正阈值取默认 5",
			input:       models.Settings{IdleSchedulingEnabled: true, IdleThresholdMinutes: 0},
			wantEnabled: true, wantThreshold: 5,
		},
		{
			name: "非法时间窗归一成空",
			input: models.Settings{
				IdleSchedulingEnabled: true, IdleThresholdMinutes: 30, IdleRequireACPower: true,
				IdleWindowStart: "25:00", IdleWindowEnd: "晚上",
			},
			wantEnabled: true, wantThreshold: 30, wantRequireAC: true,
		},
		{
			name:        "关掉开关如实保存",
			input:       models.Settings{IdleSchedulingEnabled: false, IdleThresholdMinutes: 5},
			wantEnabled: false, wantThreshold: 5,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := testCase.input
			input.VideoExtensions = ".mp4"
			input.PlayWeight = 2
			if err := service.UpdateSettings(input); err != nil {
				t.Fatalf("保存设置失败: %v", err)
			}
			saved, err := service.GetSettings()
			if err != nil {
				t.Fatalf("读取设置失败: %v", err)
			}
			if saved.IdleSchedulingEnabled != testCase.wantEnabled {
				t.Fatalf("开关错误: got=%v want=%v", saved.IdleSchedulingEnabled, testCase.wantEnabled)
			}
			if saved.IdleThresholdMinutes != testCase.wantThreshold {
				t.Fatalf("阈值错误: got=%d want=%d", saved.IdleThresholdMinutes, testCase.wantThreshold)
			}
			if saved.IdleRequireACPower != testCase.wantRequireAC {
				t.Fatalf("接电源要求错误: got=%v want=%v", saved.IdleRequireACPower, testCase.wantRequireAC)
			}
			if saved.IdleWindowStart != testCase.wantWindowFrom || saved.IdleWindowEnd != testCase.wantWindowTo {
				t.Fatalf("时间窗错误: got=[%q,%q] want=[%q,%q]",
					saved.IdleWindowStart, saved.IdleWindowEnd, testCase.wantWindowFrom, testCase.wantWindowTo)
			}
		})
	}
}

// 空闲门读的是设置表里的当前值：改了设置，下一次读取（缓存过期后）就得跟上。
func TestIdleGateReadsSettingsFromDatabase(t *testing.T) {
	database.DB = dbtest.Open(t)
	if err := database.DB.Create(&models.Settings{
		VideoExtensions: ".mp4", PlayWeight: 2,
		IdleSchedulingEnabled: true, IdleThresholdMinutes: 7, IdleWindowStart: "22:00", IdleWindowEnd: "06:00",
	}).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}

	gate := NewIdleGate()
	gate.SetProbe(staticIdleProbe(0, true))
	settings := gate.currentSettings()
	if !settings.Enabled || settings.ThresholdMinutes != 7 || settings.WindowStart != "22:00" || settings.WindowEnd != "06:00" {
		t.Fatalf("门读到的设置错误: %+v", settings)
	}
}
