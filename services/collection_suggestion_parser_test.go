package services

import "testing"

// 解析规则是行为契约（D-023）：五种模式、优先级、去噪、全角数字与不命中
// 全部钉在这张表里。改正则必须先改这里。
func TestParseEpisodeFromFileNameTable(t *testing.T) {
	cases := []struct {
		name       string
		stem       string
		wantOK     bool
		wantSeries string
		wantSeason *int
		wantEpisod int
	}{
		{
			name:       "S01E02 同时给出季与集",
			stem:       "The.Expanse.S01E02.1080p.WEB-DL.x265",
			wantOK:     true,
			wantSeries: "the expanse",
			wantSeason: intPtr(1),
			wantEpisod: 2,
		},
		{
			name:       "S01E02 小写同样命中",
			stem:       "the expanse s01e02",
			wantOK:     true,
			wantSeries: "the expanse",
			wantSeason: intPtr(1),
			wantEpisod: 2,
		},
		{
			name:       "第 N 集只给集号",
			stem:       "琅琊榜 第 12 集",
			wantOK:     true,
			wantSeries: "琅琊榜",
			wantEpisod: 12,
		},
		{
			name:       "第 N 话 与 第 N 話 同样命中",
			stem:       "钢之炼金术师 第7話",
			wantOK:     true,
			wantSeries: "钢之炼金术师",
			wantEpisod: 7,
		},
		{
			name:       "EP 前缀",
			stem:       "Dark_Matter_EP08",
			wantOK:     true,
			wantSeries: "dark matter",
			wantEpisod: 8,
		},
		{
			name:       "单个 E 前缀",
			stem:       "Dark Matter E9",
			wantOK:     true,
			wantSeries: "dark matter",
			wantEpisod: 9,
		},
		{
			name:       "方括号集号在去掉组名之后仍然可用",
			stem:       "[Nekomoe] Frieren [05][1080p]",
			wantOK:     true,
			wantSeries: "frieren",
			wantEpisod: 5,
		},
		{
			name:       "片名整个在方括号里：取紧邻集号之前的那个括号当片名（中文）",
			stem:       "[字幕组][葬送的芙莉莲][05][1080p]",
			wantOK:     true,
			wantSeries: "葬送的芙莉莲",
			wantEpisod: 5,
		},
		{
			name:       "片名整个在方括号里：取紧邻集号之前的那个括号当片名（拉丁）",
			stem:       "[Sakurato][Frieren][05][1080p]",
			wantOK:     true,
			wantSeries: "frieren",
			wantEpisod: 5,
		},
		{
			name:       "片名括号在质量标签之后：跳过标签括号仍取到片名",
			stem:       "[组名][片名][1080p][05]",
			wantOK:     true,
			wantSeries: "片名",
			wantEpisod: 5,
		},
		{
			name:       "多个组名括号时取最靠近集号的那个",
			stem:       "[Nekomoe kissaten][Frieren][05][1080p][JPSC]",
			wantOK:     true,
			wantSeries: "frieren",
			wantEpisod: 5,
		},
		{
			name:   "只有组名与集号两个括号：第一个括号是发布组名，不当片名",
			stem:   "[组名][05]",
			wantOK: false,
		},
		{
			name:   "只有集号一个括号：没有片名可取",
			stem:   "[05]",
			wantOK: false,
		},
		{
			name:   "集号之前只剩纯数字与标签括号：仍不成候选",
			stem:   "[2019][1080p][05]",
			wantOK: false,
		},
		{
			name:       "空格短横线空格结尾的集号",
			stem:       "Sousou no Frieren - 03",
			wantOK:     true,
			wantSeries: "sousou no frieren",
			wantEpisod: 3,
		},
		{
			name:       "同集多版本：03v2 与 03 解析出同一集",
			stem:       "Sousou no Frieren - 03v2",
			wantOK:     true,
			wantSeries: "sousou no frieren",
			wantEpisod: 3,
		},
		{
			name:       "全角数字规范化为半角",
			stem:       "庆余年 第０３集",
			wantOK:     true,
			wantSeries: "庆余年",
			wantEpisod: 3,
		},
		{
			name:       "跨季：同名系列的第二季",
			stem:       "The.Expanse.S02E01.2160p.HEVC",
			wantOK:     true,
			wantSeries: "the expanse",
			wantSeason: intPtr(2),
			wantEpisod: 1,
		},
		{
			name:       "去噪：质量标签不进系列名",
			stem:       "Chernobyl.1080p.BluRay.x264.DTS-HD - 04",
			wantOK:     true,
			wantSeries: "chernobyl",
			wantEpisod: 4,
		},
		{
			name:   "没有任何剧集模式：不命中",
			stem:   "Interstellar (2014) 2160p",
			wantOK: false,
		},
		{
			name:   "只有集号没有系列名：不命中",
			stem:   "S01E01",
			wantOK: false,
		},
		{
			name:       "优先级：SxxExx 高于方括号集号",
			stem:       "Show [07] S03E11",
			wantOK:     true,
			wantSeries: "show [07]",
			wantSeason: intPtr(3),
			wantEpisod: 11,
		},
		{
			name:       "优先级：第 N 集 高于 E 前缀",
			stem:       "剧名 E5 第 6 集",
			wantOK:     true,
			wantSeries: "剧名 e5",
			wantEpisod: 6,
		},
		{
			name:       "误判边界：年份加短横线集号仍然成立，靠 ≥2 与同名兜底",
			stem:       "Video 2019 - 12",
			wantOK:     true,
			wantSeries: "video 2019",
			wantEpisod: 12,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed := ParseEpisodeFromFileName(testCase.stem)
			if parsed.OK != testCase.wantOK {
				t.Fatalf("命中与否不符: got=%v want=%v parsed=%#v", parsed.OK, testCase.wantOK, parsed)
			}
			if !testCase.wantOK {
				return
			}
			if parsed.NormalizedSeries != testCase.wantSeries {
				t.Fatalf("系列名不符: got=%q want=%q", parsed.NormalizedSeries, testCase.wantSeries)
			}
			if parsed.Episode == nil || *parsed.Episode != testCase.wantEpisod {
				t.Fatalf("集号不符: got=%v want=%d", parsed.Episode, testCase.wantEpisod)
			}
			if testCase.wantSeason == nil {
				if parsed.Season != nil {
					t.Fatalf("不该解析出季号: got=%d", *parsed.Season)
				}
			} else if parsed.Season == nil || *parsed.Season != *testCase.wantSeason {
				t.Fatalf("季号不符: got=%v want=%d", parsed.Season, *testCase.wantSeason)
			}
		})
	}
}

// 系列名保留原始大小写供展示，规范化名只用于分组。
func TestParseEpisodeKeepsDisplayCaseAndLowercasesGroupKey(t *testing.T) {
	parsed := ParseEpisodeFromFileName("The.Expanse.S01E02")
	if !parsed.OK {
		t.Fatalf("应当命中: %#v", parsed)
	}
	if parsed.Series != "The Expanse" {
		t.Fatalf("展示用系列名应保留大小写: %q", parsed.Series)
	}
	if parsed.NormalizedSeries != "the expanse" {
		t.Fatalf("分组键应为小写: %q", parsed.NormalizedSeries)
	}
}

func intPtr(value int) *int { return &value }
