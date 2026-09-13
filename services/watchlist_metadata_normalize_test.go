package services

import "testing"

// 番号归一化只统一书写形态：去空白、转大写、分隔符收敛成一个半角连字符。
func TestNormalizeWatchlistAVCode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"已经是标准形态", "ABC-123", "ABC-123"},
		{"小写转大写", "abc-123", "ABC-123"},
		{"首尾空白", "  ABC-123\t", "ABC-123"},
		{"空格当分隔符", "ABC 123", "ABC-123"},
		{"下划线当分隔符", "abc_123", "ABC-123"},
		{"全角连字符", "ABC－123", "ABC-123"},
		{"中文破折号", "ABC—123", "ABC-123"},
		{"连续分隔符折成一个", "ABC - _ 123", "ABC-123"},
		{"首尾分隔符丢掉", "-ABC-123-", "ABC-123"},
		{"没有分隔符的番号不硬插", "ABC123", "ABC123"},
		{"多段番号保留每段分隔", "ABC-123-C", "ABC-123-C"},
		{"空串", "", ""},
		{"只有空白", "   ", ""},
		{"只有分隔符", "---", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeWatchlistAVCode(tc.in); got != tc.want {
				t.Errorf("normalizeWatchlistAVCode(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// 承重约定：**不做**补零、去零这类源专有编码换算。
//
// 单独立一条而不是混进上面的表格，是因为它钉的不是「某个输入产出某个输出」，
// 而是一条设计裁决：谁想加换算规则，必须先来改掉这条测试和它的理由。
func TestNormalizeWatchlistAVCodeDoesNotReencode(t *testing.T) {
	// FANZA 那套补零形态（abc00123）即便传进来，也只做大小写与分隔符处理，
	// 不还原成 ABC-123——凭猜换算会在用户看不见的地方查错片。
	if got := normalizeWatchlistAVCode("abc00123"); got != "ABC00123" {
		t.Errorf("normalizeWatchlistAVCode(\"abc00123\") = %q，期望 %q（不得补零/去零）", got, "ABC00123")
	}
	// 反向同理：标准形态不会被补成源站的内部编码。
	if got := normalizeWatchlistAVCode("ABC-123"); got != "ABC-123" {
		t.Errorf("normalizeWatchlistAVCode(\"ABC-123\") = %q，期望原样", got)
	}
}
