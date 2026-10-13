package models

import (
	"encoding/hex"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"gorm.io/gorm"
)

// 片名排序键（UI 二期 §2.3，D-P2-01）：videos.title_sort_key 存 display_title（为空时取去掉扩展名的 name）
// 在中文排序规则下的排序键（golang.org/x/text/collate，language.Chinese = 拼音序，忽略大小写与全半角），
// 十六进制存储。两种数据库按字节序比较十六进制串，顺序与排序规则一致，游标因此在两边通用。
//
// 已知限制：CLDR 的拼音排序给每个汉字只配一个读音，多音字按它选定的那个读音排（「长安」的「长」排在 zhang，
// 「重庆」的「重」排在 zhong）。这是排序规则表本身的取舍，这里不做逐词纠音。
//
// 维护分三条路：
//   - 新建（扫描建档、工作台与超分产物）与 Save 整行写回：BeforeCreate / BeforeUpdate 钩子按结构体算；
//   - 改 display_title / name 的 map 更新：一律经 services.UpdateVideoTitleFields，它读出这一行的现值、
//     合上新值算好键，随同一条 UPDATE 写入；
//   - 存量与漏网的行（键为空串）：启动时后台按 500 一批补齐（services.BackfillVideoTitleSortKeys）。

// titleSortKeyMaxBytes 是排序键截断前的字节上限（hex 后 512 字符，正好是列宽）。中文每字约 3 字节主权重，
// 截断只发生在八十多个字之后；截断后与更长的同前缀片名的先后由 id 决定，对排序无实际影响。
const titleSortKeyMaxBytes = 256

type titleCollator struct {
	collator *collate.Collator
	buffer   collate.Buffer
}

// Collator 带内部缓冲，不能并发使用：每个 goroutine 从池里借一个。
var titleCollators = sync.Pool{New: func() any {
	return &titleCollator{collator: collate.New(language.Chinese, collate.IgnoreCase, collate.IgnoreWidth)}
}}

// VideoTitleSortSource 是参与排序的片名：display_title，为空时取去掉扩展名的文件名。
func VideoTitleSortSource(displayTitle, name string) string {
	if displayTitle != "" {
		return strings.TrimSpace(displayTitle)
	}
	return strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name)))
}

// VideoTitleSortKey 算出 title_sort_key。两列都为空时返回空串（与「待补齐」同值，补齐时会再算一次、仍为空）。
func VideoTitleSortKey(displayTitle, name string) string {
	source := VideoTitleSortSource(displayTitle, name)
	if source == "" {
		return ""
	}
	tc := titleCollators.Get().(*titleCollator)
	key := tc.collator.KeyFromString(&tc.buffer, source)
	if len(key) > titleSortKeyMaxBytes {
		key = key[:titleSortKeyMaxBytes]
	}
	encoded := hex.EncodeToString(key)
	tc.buffer.Reset()
	titleCollators.Put(tc)
	return encoded
}

// ErrVideoTitleSortKeyBypass 表示有一条按条件（而不是按已加载的行）更新 display_title / name 的写入没有经过
// services.UpdateVideoTitleFields，钩子算不出那些行的排序键。只在 StrictVideoTitleSortKey 打开时返回（测试）；
// 生产上把键清成空串，留给下次启动的补齐，并记一行日志。
var ErrVideoTitleSortKeyBypass = errors.New("videos: 按条件更新 display_title / name 必须经 services.UpdateVideoTitleFields")

// StrictVideoTitleSortKey 让上面那种漏网写入直接报错。services 的测试把它打开，整套用例因此兼作「是否还有写入
// 路径绕过排序键」的探测器；生产代码不碰它。
var StrictVideoTitleSortKey = false

// BeforeCreate 新建视频时按结构体的两列算排序键（批量 Create 时 GORM 逐个元素调用）。
func (v *Video) BeforeCreate(tx *gorm.DB) error {
	v.TitleSortKey = VideoTitleSortKey(v.DisplayTitle, v.Name)
	return nil
}

// BeforeUpdate 让改了 display_title / name 的更新同时写对 title_sort_key：
//   - Save(&video)（Dest 就是这一行）：整行写回，按结构体算；
//   - 更新内容里已经带了 title_sort_key（经 services.UpdateVideoTitleFields）：不动；
//   - 按已加载的行更新（Model(&video)，主键非零）：读这一行的现值补齐另一列，用 SetColumn 写入；
//   - 按条件更新（Model(&Video{}).Where(...)）：钩子不知道是哪几行，见 ErrVideoTitleSortKeyBypass。
func (v *Video) BeforeUpdate(tx *gorm.DB) error {
	stmt := tx.Statement
	if dest, ok := stmt.Dest.(*Video); ok && dest == v {
		v.TitleSortKey = VideoTitleSortKey(v.DisplayTitle, v.Name)
		return nil
	}
	display, name, keyGiven, known := videoTitleChanges(stmt.Dest)
	if keyGiven || (display == nil && name == nil && known) {
		return nil
	}
	if !known || v.ID == 0 {
		if StrictVideoTitleSortKey {
			return ErrVideoTitleSortKeyBypass
		}
		log.Printf("videos title_sort_key: %v；本次写入把排序键清空，等下次启动补齐", ErrVideoTitleSortKeyBypass)
		stmt.SetColumn("TitleSortKey", "")
		return nil
	}
	if display == nil || name == nil {
		var current struct {
			DisplayTitle string
			Name         string
		}
		err := tx.Session(&gorm.Session{NewDB: true}).Unscoped().Model(&Video{}).
			Select("display_title", "name").Where("id = ?", v.ID).Take(&current).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if display == nil {
			display = &current.DisplayTitle
		}
		if name == nil {
			name = &current.Name
		}
	}
	stmt.SetColumn("TitleSortKey", VideoTitleSortKey(*display, *name))
	return nil
}

// videoTitleChanges 从更新内容里取出 display_title / name 的新值（没改的为 nil）与是否已带排序键。
// known=false 表示更新内容的形状认不出来，或者标题列的新值不是字符串（例如 gorm.Expr）。
func videoTitleChanges(dest any) (display, name *string, keyGiven, known bool) {
	take := func(value any) (*string, bool) {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		return &text, true
	}
	switch updates := dest.(type) {
	case map[string]interface{}:
		for key, value := range updates {
			switch key {
			case "title_sort_key", "TitleSortKey":
				keyGiven = true
			case "display_title", "DisplayTitle":
				text, ok := take(value)
				if !ok {
					return nil, nil, false, false
				}
				display = text
			case "name", "Name":
				text, ok := take(value)
				if !ok {
					return nil, nil, false, false
				}
				name = text
			}
		}
		return display, name, keyGiven, true
	case Video:
		return videoStructTitleChanges(&updates)
	case *Video:
		return videoStructTitleChanges(updates)
	}
	return nil, nil, false, false
}

// videoStructTitleChanges 对应 Updates(Video{...})：GORM 只写非零字段。
func videoStructTitleChanges(updates *Video) (display, name *string, keyGiven, known bool) {
	if updates.TitleSortKey != "" {
		keyGiven = true
	}
	if updates.DisplayTitle != "" {
		display = &updates.DisplayTitle
	}
	if updates.Name != "" {
		name = &updates.Name
	}
	return display, name, keyGiven, true
}
