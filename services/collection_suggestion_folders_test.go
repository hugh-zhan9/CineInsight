package services

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 按文件夹建议作品集（2026-10-14 修订 D-023）的形状表：每一行是扫描根下的一组相对路径，
// 期望的候选写成「文件夹 | 名称 | 成员相对路径（按顺序）」。
type wantFolderGroup struct {
	folders []string
	name    string
	members []string
}

const folderTestRoot = "/library"

func folderTestVideos(paths []string) []folderSuggestionVideo {
	videos := make([]folderSuggestionVideo, 0, len(paths))
	for index, rel := range paths {
		name := filepath.Base(rel)
		videos = append(videos, folderSuggestionVideo{
			ID:     uint(index + 1),
			Name:   name,
			Path:   filepath.Join(folderTestRoot, filepath.FromSlash(rel)),
			Parsed: ParseEpisodeFromFileName(fileNameStem(name)),
		})
	}
	return videos
}

func folderTestPersons(names ...string) map[string]struct{} {
	persons := make(map[string]struct{}, len(names))
	for _, name := range names {
		persons[normalizeLocalMetadataName(name)] = struct{}{}
	}
	return persons
}

func runFolderGroups(paths []string, persons ...string) ([]folderSuggestionGroup, []wantFolderGroup) {
	groups := buildFolderSuggestionGroups(folderTestRoot, folderTestVideos(paths), folderTestPersons(persons...))
	got := make([]wantFolderGroup, 0, len(groups))
	for _, group := range groups {
		members := make([]string, 0, len(group.Members))
		for _, member := range group.Members {
			members = append(members, paths[member.VideoID-1])
		}
		got = append(got, wantFolderGroup{folders: group.Folders, name: group.Name, members: members})
	}
	return groups, got
}

func TestBuildFolderSuggestionGroupsShapes(t *testing.T) {
	cases := []struct {
		name    string
		paths   []string
		persons []string
		want    []wantFolderGroup
	}{
		{
			name: "课程父目录 + 中文数字章节子目录：一组，按章节号再按文件顺序",
			paths: []string{
				"训练营/第十四周毕业/毕业答辩.mp4", "训练营/第二周/a.mp4", "训练营/第一周/02 环境.mp4",
				"训练营/第十一周/x.mp4", "训练营/第一周/01 开营.mp4", "训练营/第十三周Go语言实践Runtime/z.mp4",
			},
			want: []wantFolderGroup{{folders: []string{"训练营"}, name: "训练营", members: []string{
				"训练营/第一周/01 开营.mp4", "训练营/第一周/02 环境.mp4", "训练营/第二周/a.mp4",
				"训练营/第十一周/x.mp4", "训练营/第十三周Go语言实践Runtime/z.mp4", "训练营/第十四周毕业/毕业答辩.mp4",
			}}},
		},
		{
			name:  "「序号 + 说明」的子文件夹有两个以上才当章节，并进父目录按序号排",
			paths: []string{"课程/10-实战/c.mp4", "课程/02-进阶篇/b.mp4", "课程/01-基础篇/a2.mp4", "课程/01-基础篇/a1.mp4", "课程/02-进阶篇/b0.mp4"},
			want: []wantFolderGroup{{folders: []string{"课程"}, name: "课程", members: []string{
				"课程/01-基础篇/a1.mp4", "课程/01-基础篇/a2.mp4", "课程/02-进阶篇/b.mp4", "课程/02-进阶篇/b0.mp4", "课程/10-实战/c.mp4",
			}}},
		},
		{
			name:  "扫描根下按序号排的子文件夹各自成组，不当根的章节",
			paths: []string{"01-Go/a.mp4", "01-Go/b.mp4", "02-Rust/c.mp4", "02-Rust/d.mp4"},
			want: []wantFolderGroup{
				{folders: []string{"01-Go"}, name: "01-Go", members: []string{"01-Go/a.mp4", "01-Go/b.mp4"}},
				{folders: []string{"02-Rust"}, name: "02-Rust", members: []string{"02-Rust/c.mp4", "02-Rust/d.mp4"}},
			},
		},
		{
			name:  "一个文件夹里的多集：按序号自然排序",
			paths: []string{"算法面试通关40讲/10丨递归.mp4", "算法面试通关40讲/01丨开篇.mp4", "算法面试通关40讲/2丨数组.mp4"},
			want: []wantFolderGroup{{folders: []string{"算法面试通关40讲"}, name: "算法面试通关40讲", members: []string{
				"算法面试通关40讲/01丨开篇.mp4", "算法面试通关40讲/2丨数组.mp4", "算法面试通关40讲/10丨递归.mp4",
			}}},
		},
		{
			name: "发布目录名收拾成片名",
			paths: []string{
				"Immortal.Doctor.In.Modern.City.S01.1080p.iQIYI.WEB-DL.AAC2.0.H.264-MWeb/Immortal.Doctor.In.Modern.City.S01E02.mkv",
				"Immortal.Doctor.In.Modern.City.S01.1080p.iQIYI.WEB-DL.AAC2.0.H.264-MWeb/Immortal.Doctor.In.Modern.City.S01E01.mkv",
			},
			want: []wantFolderGroup{{
				folders: []string{"Immortal.Doctor.In.Modern.City.S01.1080p.iQIYI.WEB-DL.AAC2.0.H.264-MWeb"},
				name:    "Immortal Doctor In Modern City",
				members: []string{
					"Immortal.Doctor.In.Modern.City.S01.1080p.iQIYI.WEB-DL.AAC2.0.H.264-MWeb/Immortal.Doctor.In.Modern.City.S01E01.mkv",
					"Immortal.Doctor.In.Modern.City.S01.1080p.iQIYI.WEB-DL.AAC2.0.H.264-MWeb/Immortal.Doctor.In.Modern.City.S01E02.mkv",
				},
			}},
		},
		{
			name:  "一片一目录的发布包装不成候选；装它们的 Downloads 也不成",
			paths: []string{"Downloads/Movie.A.2019.1080p/Movie.A.2019.1080p.mkv", "Downloads/Movie.B.2020.WEB-DL/Movie.B.mkv"},
		},
		{
			name:  "发布包装在课程目录里是透明的：唯一的视频上提成员",
			paths: []string{"Go进阶/01 开篇.mp4", "Go进阶/From-Telegram-20211204T015145791Z/02 并发.mp4"},
			want: []wantFolderGroup{{folders: []string{"Go进阶"}, name: "Go进阶", members: []string{
				"Go进阶/01 开篇.mp4", "Go进阶/From-Telegram-20211204T015145791Z/02 并发.mp4",
			}}},
		},
		{
			name: "分类容器：子文件夹各自成组，自己不成；散放文件留给文件名规则",
			paths: []string{
				"主播合集/随手录1.mp4", "主播合集/随手录2.mp4", "主播合集/杂谈.mp4",
				"主播合集/主播A/开播.mp4", "主播合集/主播A/下播.mp4", "主播合集/主播B/一.mp4", "主播合集/主播B/二.mp4",
			},
			want: []wantFolderGroup{
				{folders: []string{"主播合集/主播A"}, name: "主播A", members: []string{"主播合集/主播A/下播.mp4", "主播合集/主播A/开播.mp4"}},
				{folders: []string{"主播合集/主播B"}, name: "主播B", members: []string{"主播合集/主播B/一.mp4", "主播合集/主播B/二.mp4"}},
			},
		},
		{
			name: "无意义的名字一律不成候选",
			paths: []string{
				"新建文件夹/a.mp4", "新建文件夹/b.mp4", "New Folder 2/a.mp4", "New Folder 2/b.mp4",
				"untitled folder/a.mp4", "untitled folder/b.mp4", "From-Telegram-20211204T015145791Z/a.mp4",
				"From-Telegram-20211204T015145791Z/b.mp4", "2021-12-04/a.mp4", "2021-12-04/b.mp4",
				"20211204/a.mp4", "20211204/b.mp4", "归档/a.mp4", "归档/b.mp4", "misc/a.mp4", "misc/b.mp4",
			},
		},
		{
			name:    "人物名（显示名或原名）的文件夹不当作品集，里面的子文件夹照常判断",
			persons: []string{"张三", "Tom Hanks"},
			paths: []string{
				"张三/自拍1.mp4", "张三/自拍2.mp4", "Tom.Hanks/a.mp4", "Tom.Hanks/b.mp4",
				"张三/某剧/01.mp4", "张三/某剧/02.mp4",
			},
			want: []wantFolderGroup{{folders: []string{"张三/某剧"}, name: "某剧", members: []string{"张三/某剧/01.mp4", "张三/某剧/02.mp4"}}},
		},
		{
			name:  "扫描根本身永远不成候选",
			paths: []string{"a.mp4", "b.mp4", "c.mp4"},
		},
		{
			name: "同名的分季发布目录合并成一组，按季排",
			paths: []string{
				"Show.Name.S02.1080p.WEB-DL.AAC2.0.H.264-Grp/Show.Name.S02E02.mkv",
				"Show.Name.S02.1080p.WEB-DL.AAC2.0.H.264-Grp/Show.Name.S02E01.mkv",
				"Show.Name.S01.720p-Grp/Show.Name.S01E02.mkv", "Show.Name.S01.720p-Grp/Show.Name.S01E01.mkv",
			},
			want: []wantFolderGroup{{
				folders: []string{"Show.Name.S01.720p-Grp", "Show.Name.S02.1080p.WEB-DL.AAC2.0.H.264-Grp"},
				name:    "Show Name",
				members: []string{
					"Show.Name.S01.720p-Grp/Show.Name.S01E01.mkv", "Show.Name.S01.720p-Grp/Show.Name.S01E02.mkv",
					"Show.Name.S02.1080p.WEB-DL.AAC2.0.H.264-Grp/Show.Name.S02E01.mkv",
					"Show.Name.S02.1080p.WEB-DL.AAC2.0.H.264-Grp/Show.Name.S02E02.mkv",
				},
			}},
		},
		{
			name:  "只是同名、位置不同又没有季号的文件夹各自成组",
			paths: []string{"A剧/Extras/1.mp4", "A剧/Extras/2.mp4", "B剧/Extras/1.mp4", "B剧/Extras/2.mp4"},
			want: []wantFolderGroup{
				{folders: []string{"A剧/Extras"}, name: "Extras", members: []string{"A剧/Extras/1.mp4", "A剧/Extras/2.mp4"}},
				{folders: []string{"B剧/Extras"}, name: "Extras", members: []string{"B剧/Extras/1.mp4", "B剧/Extras/2.mp4"}},
			},
		},
		{
			name:  "Sample 子目录里的片段不上提",
			paths: []string{"Show.S01.1080p/Show.S01E02.mkv", "Show.S01.1080p/Sample/sample.mkv", "Show.S01.1080p/Show.S01E01.mkv"},
			want: []wantFolderGroup{{folders: []string{"Show.S01.1080p"}, name: "Show", members: []string{
				"Show.S01.1080p/Show.S01E01.mkv", "Show.S01.1080p/Show.S01E02.mkv",
			}}},
		},
		{
			name:  "Season N / CD N 子目录也是章节",
			paths: []string{"某剧/Season 2/b.mkv", "某剧/Season 1/a.mkv", "Film/CD2/part.mkv", "Film/CD1/part.mkv"},
			want: []wantFolderGroup{
				{folders: []string{"Film"}, name: "Film", members: []string{"Film/CD1/part.mkv", "Film/CD2/part.mkv"}},
				{folders: []string{"某剧"}, name: "某剧", members: []string{"某剧/Season 1/a.mkv", "某剧/Season 2/b.mkv"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := runFolderGroups(tc.paths, tc.persons...)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("分组不符\n got=%#v\nwant=%#v", got, tc.want)
			}
		})
	}
}

// 章节号写进 Season、开头序号写进 Episode，位置稠密且只在真同集多版本时共用。
func TestBuildFolderSuggestionGroupsSeasonEpisodeAndPositions(t *testing.T) {
	groups, _ := runFolderGroups([]string{
		"训练营/第二周/03 作业.mp4", "训练营/第一周/01 开营.mp4", "训练营/第一周/01 补充.mp4", "训练营/开营直播.mp4",
	})
	if len(groups) != 1 {
		t.Fatalf("应当只有一组: %#v", groups)
	}
	type row struct{ season, episode, position int }
	var got []row
	for _, member := range groups[0].Members {
		got = append(got, row{intValueOrZero(member.Season), intValueOrZero(member.Episode), member.Position})
	}
	// 本层散放的「开营直播」在章节之前；两个「01」只靠开头序号，不算同集多版本。
	want := []row{{0, 0, 1}, {1, 1, 2}, {1, 1, 3}, {2, 3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("季/集/位置不符: got=%v want=%v", got, want)
	}

	versions, _ := runFolderGroups([]string{"Show/Show - 04.mkv", "Show/Show - 03v2.mkv", "Show/Show - 03.mkv"})
	var positions []int
	for _, member := range versions[0].Members {
		positions = append(positions, member.Position)
	}
	if !reflect.DeepEqual(positions, []int{1, 1, 2}) {
		t.Fatalf("同集多版本应共用位置: %v", positions)
	}
	if versions[0].NormalizedKey != "dir:show" {
		t.Fatalf("记忆键不符: %q", versions[0].NormalizedKey)
	}
}

func TestParseChineseNumeralAndChapterNames(t *testing.T) {
	numerals := map[string]int{"一": 1, "九": 9, "十": 10, "十一": 11, "十四": 14, "二十": 20, "九十": 90, "一百零五": 105}
	for text, want := range numerals {
		if got, ok := parseChineseNumeral(text); !ok || got != want {
			t.Fatalf("中文数字 %q: got=%d ok=%v want=%d", text, got, ok, want)
		}
	}
	chapters := map[string]int{
		"第一周": 1, "第十四周毕业": 14, "第十三周Go语言实践Runtime": 13, "第3章": 3, "第二季": 2, "第五单元": 5,
		"Season 2": 2, "season.03": 3, "S01": 1, "Part 2": 2, "Disc 1": 1, "CD2": 2, "Vol.4": 4, "07": 7,
	}
	for name, want := range chapters {
		if got, ok := folderChapterNumber(name); !ok || got != want {
			t.Fatalf("章节 %q: got=%d ok=%v want=%d", name, got, ok, want)
		}
	}
	for _, name := range []string{"Show.Name.S01.1080p", "S01E01", "算法面试通关40讲", "Partition 1", "2021", "训练营"} {
		if _, ok := folderChapterNumber(name); ok {
			t.Fatalf("%q 不该算章节", name)
		}
	}
}

func TestCleanFolderSuggestionName(t *testing.T) {
	cases := map[string]string{
		"Immortal.Doctor.In.Modern.City.S01.1080p.iQIYI.WEB-DL.AAC2.0.H.264-MWeb": "Immortal Doctor In Modern City",
		"[字幕组] 某番 [1080p]":         "某番",
		"[某番]":                     "某番",
		"Show Name (2019)":         "Show Name",
		"Show_Name.Season.2.1080p": "Show Name",
		"【公众号：某某】算法面试通关40讲": "算法面试通关40讲",
		"某剧 第二季":                           "某剧",
		"Movie.Title.2160p.HDR.x265-Group": "Movie Title",
	}
	for raw, want := range cases {
		if got := cleanFolderSuggestionName(raw); got != want {
			t.Fatalf("收拾 %q: got=%q want=%q", raw, got, want)
		}
	}
}

// 相对路径太长时记忆键截断并接哈希：不超过 size:200、结果确定、不同路径不撞。
func TestFolderSuggestionKeyIsBounded(t *testing.T) {
	long := strings.Repeat("很长的目录名", 20)
	first, again, other := folderSuggestionKey(long+"/A"), folderSuggestionKey(long+"/A"), folderSuggestionKey(long+"/B")
	if len(first) > folderSuggestionKeyMaxBytes || !strings.HasPrefix(first, folderSuggestionKeyPrefix) {
		t.Fatalf("记忆键越界: len=%d %q", len(first), first)
	}
	if first != again || first == other {
		t.Fatalf("记忆键应确定且区分路径: %q %q %q", first, again, other)
	}
}
