//go:build darwin

package services

import (
	"testing"
	"time"
)

// ioreg 的输出解析：只认 HIDIdleTime，取纳秒；认不出来就报错，
// 绝不猜一个"大概空闲"的值（猜错的方向是让后台任务在用户干活时开跑）。
func TestParseHIDIdleTime(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		want    time.Duration
		wantErr bool
	}{
		{
			name:   "典型输出",
			output: "  +-o IOHIDSystem  <class IOHIDSystem>\n    {\n      \"HIDIdleTime\" = 42000000000\n      \"HIDPointerAcceleration\" = 3\n    }\n",
			want:   42 * time.Second,
		},
		{name: "无空格写法", output: `"HIDIdleTime"=1500000000`, want: 1500 * time.Millisecond},
		{name: "刚刚有输入", output: `"HIDIdleTime" = 0`, want: 0},
		{name: "输出里没有这个键", output: "+-o IOHIDSystem\n", wantErr: true},
		{name: "空输出", output: "", wantErr: true},
		{name: "非数字", output: `"HIDIdleTime" = abc`, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseHIDIdleTime(testCase.output)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("应报错，实际得到 %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("空闲时长错误: got=%v want=%v", got, testCase.want)
			}
		})
	}
}

// pmset 的输出解析：判"有没有 Battery Power"，台式机（没有电池）不能被误报成靠电池。
func TestParseACPower(t *testing.T) {
	cases := []struct {
		name   string
		output string
		wantAC bool
	}{
		{"接着电源", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true\n", true},
		{"靠电池", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t82%; discharging; 4:41 remaining present: true\n", false},
		{"台式机没有电池", "Now drawing from 'AC Power'\nNo batteries available.\n", true},
		{"输出为空", "", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := parseACPower(testCase.output); got != testCase.wantAC {
				t.Fatalf("供电判定错误: got=%v want=%v", got, testCase.wantAC)
			}
		})
	}
}
