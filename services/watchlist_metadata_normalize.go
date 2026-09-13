package services

import (
	"strings"
	"unicode"
)

// 番号归一化（D-AVM05）。只做**书写形态**的统一，不做任何源专有编码换算。
//
// 三家 av 源（JavBus、jav321、airav）用的都是同一种展示形态 ABC-123，所以把用户
// 手输的各种写法收敛到这一种就够了：去空白、转大写、把分隔符统一成半角连字符。
//
// **明确不做**补零、去零、厂牌前缀补全这类换算。这条约定继承自 FANZA 适配器留下的
// 裁决——它的 content_id 是另一套补零形态（abc00123），而换算规则没有公开文档可依，
// 凭猜写一个只会在用户看不见的地方悄悄查错片。FANZA 退场后那套编码不再出现，
// 但禁止凭猜换算这条规矩照旧。
//
// 归一化只作用于 av：其余类型的匹配键是片名，大小写与分隔符都是有意义的。

// normalizeWatchlistAVCode 把用户手输的番号收敛成 ABC-123 形态。
//
// 输入为空或收敛后为空时原样返回空串——「番号为空」由各适配器按自己的规矩认定，
// 这里不代它判断。
func normalizeWatchlistAVCode(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(trimmed))
	// pendingSeparator 把连续的分隔符折成一个，并且丢掉首尾的分隔符：
	// 只有真的还要再写字符时才补上那一个连字符。
	pendingSeparator := false
	for _, r := range trimmed {
		if isWatchlistAVCodeSeparator(r) {
			if builder.Len() > 0 {
				pendingSeparator = true
			}
			continue
		}
		if pendingSeparator {
			builder.WriteByte('-')
			pendingSeparator = false
		}
		builder.WriteRune(unicode.ToUpper(r))
	}
	return builder.String()
}

// isWatchlistAVCodeSeparator 认得出番号里可能出现的分隔写法。
//
// 覆盖半角连字符、下划线、各种空白，以及全角连字符与中日文常见的破折号——
// 输入法切换时这些很容易混进来，而它们表达的都是同一个分隔意图。
func isWatchlistAVCodeSeparator(r rune) bool {
	switch r {
	case '-', '_', '‐', '‑', '‒', '–', '—', '―', '－', '＿':
		return true
	}
	return unicode.IsSpace(r)
}
