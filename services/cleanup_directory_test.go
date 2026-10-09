package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"video-master/database"
	"video-master/models"
)

func TestCleanupExactDuplicatesKeepRelatedVideosInOneDirectory(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, directory := range []string{first, second} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	// A/B 与 C/D 各自完全重复。创建顺序与整理成果交叉，旧推荐会留下 A、D。
	a := p016Video(t, first, "A.mp4", "first-video", 1920, 1080)
	b := p016Video(t, second, "B.mp4", "first-video", 1920, 1080)
	d := p016Video(t, second, "D.mp4", "second-video", 1920, 1080)
	c := p016Video(t, first, "C.mp4", "second-video", 1920, 1080)
	p016UpdateVideo(t, a.ID, map[string]interface{}{"is_favorite": true})
	p016UpdateVideo(t, d.ID, map[string]interface{}{"is_favorite": true})

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DuplicateGroups) != 2 {
		t.Fatalf("expected two exact duplicate groups, got %+v", result.DuplicateGroups)
	}
	wantCopies := map[uint]uint{a.ID: b.ID, c.ID: d.ID}
	for _, group := range result.DuplicateGroups {
		if group.Original.Directory != first || len(group.Candidates) != 1 ||
			wantCopies[group.Original.ID] != group.Candidates[0].ID {
			t.Fatalf("both groups should retain videos in %s, got %+v", first, result.DuplicateGroups)
		}
	}
	if !result.Curation[d.ID].Favorite || !result.Curation[a.ID].Favorite {
		t.Fatal("directory preference must retain all curation data for review and merging")
	}
	for _, video := range []models.Video{a, b, c, d} {
		if _, err := os.Stat(video.Path); err != nil {
			t.Fatalf("analysis changed a source file: %v", err)
		}
	}
}

func TestCleanupExactDirectoryRanking(t *testing.T) {
	v := func(id uint, directory string) models.Video {
		return models.Video{ID: id, Directory: directory, Width: 1920, Height: 1080, Size: 100}
	}
	tests := []struct {
		name     string
		groups   [][]models.Video
		curation map[uint]CleanupCuration
		keepers  []uint
	}{
		{
			name:    "crossed IDs stay in one directory",
			groups:  [][]models.Video{{v(1, "/a"), v(2, "/b")}, {v(3, "/b"), v(4, "/a")}},
			keepers: []uint{1, 4},
		},
		{
			name:     "complete directory takes priority over individually curated copies",
			groups:   [][]models.Video{{v(1, "/a"), v(2, "/z")}, {v(3, "/b"), v(4, "/z")}},
			curation: map[uint]CleanupCuration{1: {Favorite: true, Liked: true}, 3: {Tags: true}},
			keepers:  []uint{2, 4},
		},
		{
			name: "extra copies in one directory do not inflate coverage",
			groups: [][]models.Video{
				{v(1, "/a"), v(2, "/a"), v(3, "/a"), v(4, "/b")},
				{v(5, "/b"), v(6, "/c")}, {v(7, "/b"), v(8, "/c")},
			},
			keepers: []uint{4, 5, 7},
		},
		{
			name: "choose next directory using remaining coverage",
			groups: [][]models.Video{
				{v(1, "/a"), v(2, "/b")}, {v(3, "/a"), v(4, "/b")},
				{v(5, "/a"), v(6, "/u")}, {v(7, "/a"), v(8, "/v")},
				{v(9, "/b"), v(10, "/c")}, {v(11, "/c"), v(12, "/w")},
			},
			keepers: []uint{1, 3, 5, 7, 10, 11},
		},
		{
			name:     "equal coverage prefers curation across groups",
			groups:   [][]models.Video{{v(1, "/a"), v(2, "/z")}, {v(3, "/a"), v(4, "/z")}},
			curation: map[uint]CleanupCuration{1: {Favorite: true}, 2: {Favorite: true}, 4: {Tags: true}},
			keepers:  []uint{2, 4},
		},
		{
			name: "extra copies do not inflate curation",
			groups: [][]models.Video{
				{v(1, "/a"), v(2, "/a"), v(3, "/a"), v(4, "/z")}, {v(5, "/a"), v(6, "/z")},
			},
			curation: map[uint]CleanupCuration{1: {Tags: true}, 2: {Tags: true}, 3: {Tags: true}, 4: {Tags: true, Favorite: true}},
			keepers:  []uint{4, 6},
		},
		{
			name: "assigned groups no longer contribute curation",
			groups: [][]models.Video{
				{v(1, "/a"), v(2, "/b")}, {v(3, "/a"), v(4, "/c")},
				{v(5, "/a"), v(6, "/d")}, {v(7, "/b"), v(8, "/c")},
			},
			curation: map[uint]CleanupCuration{2: {Favorite: true, Tags: true}, 8: {Favorite: true}},
			keepers:  []uint{1, 3, 5, 8},
		},
		{
			name:     "curated copy within selected directory wins",
			groups:   [][]models.Video{{v(1, "/a"), v(2, "/a"), v(3, "/b")}, {v(4, "/a"), v(5, "/b")}},
			curation: map[uint]CleanupCuration{2: {Tags: true}},
			keepers:  []uint{2, 4},
		},
		{
			name:    "unknown directories are not one shared directory",
			groups:  [][]models.Video{{v(1, ""), v(2, "/a")}, {v(3, ""), v(4, "/z")}, {v(5, "/z"), v(6, "/b")}},
			keepers: []uint{2, 4, 5},
		},
		{
			name:     "all directories unknown keeps original ranking",
			groups:   [][]models.Video{{v(1, ""), v(2, "")}},
			curation: map[uint]CleanupCuration{2: {Favorite: true}},
			keepers:  []uint{2},
		},
		{
			name:    "subdirectories are separate even under same scan root",
			groups:  [][]models.Video{{v(1, "/root/a"), v(2, "/other/x")}, {v(3, "/root/b"), v(4, "/other/x")}},
			keepers: []uint{2, 4},
		},
		{
			name:    "path separators are normalized",
			groups:  [][]models.Video{{v(1, "/z/./"), v(2, "/a")}, {v(3, "/z"), v(4, "/b")}},
			keepers: []uint{1, 3},
		},
		{
			name:     "single group still prefers curation",
			groups:   [][]models.Video{{v(1, "/a"), v(2, "/z")}},
			curation: map[uint]CleanupCuration{2: {Favorite: true}},
			keepers:  []uint{2},
		},
		{name: "empty results"},
		{name: "single member", groups: [][]models.Video{{v(1, "/a")}}, keepers: []uint{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 覆盖 map / 输入遍历顺序变化，推荐仍按相同规则确定。
			for permutation := 0; permutation < 4; permutation++ {
				groups := make([]CleanupDuplicateGroup, 0, len(tt.groups))
				for i, original := range tt.groups {
					members := slices.Clone(original)
					if permutation&1 != 0 {
						slices.Reverse(members)
					}
					groups = append(groups, CleanupDuplicateGroup{Original: members[0], Candidates: members[1:], Reason: fmt.Sprint(i)})
				}
				if permutation&2 != 0 {
					slices.Reverse(groups)
				}
				if err := rankExactCleanupGroups(context.Background(), groups, tt.curation); err != nil {
					t.Fatal(err)
				}
				for i, original := range tt.groups {
					index := i
					if permutation&2 != 0 {
						index = len(groups) - 1 - i
					}
					group := groups[index]
					if group.Original.ID != tt.keepers[i] || group.Reason != fmt.Sprint(i) {
						t.Fatalf("permutation %d group %d: keeper %d, want %d; reason %q", permutation, i, group.Original.ID, tt.keepers[i], group.Reason)
					}
					got, want := videoIDsOf(append([]models.Video{group.Original}, group.Candidates...)), videoIDsOf(original)
					slices.Sort(got)
					slices.Sort(want)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("group membership changed: got %v, want %v", got, want)
					}
				}
			}
		})
	}
}

func TestCleanupExactDirectoryRankingCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	groups := []CleanupDuplicateGroup{{Original: models.Video{ID: 1}, Candidates: []models.Video{{ID: 2}}}}
	if err := rankExactCleanupGroups(ctx, groups, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if groups[0].Original.ID != 1 || groups[0].Candidates[0].ID != 2 {
		t.Fatal("cancelled ranking changed the input")
	}
}

func TestCleanupDirectoryPreferenceOnlyAppliesToExactDuplicates(t *testing.T) {
	setupCleanupServiceTestDB(t)
	videos := make([]models.Video, 10)
	for i := range videos {
		directory := "/a"
		if i%2 != 0 {
			directory = "/b"
		}
		videos[i] = models.Video{Path: fmt.Sprintf("%s/%d.mp4", directory, i), Directory: directory,
			Size: 100, Width: 1920, Height: 1080, IsFavorite: i == 5 || i == 7}
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	result := &CleanupAnalysis{
		DuplicateGroups: []CleanupDuplicateGroup{
			{Original: videos[0], Candidates: []models.Video{videos[1]}},
			{Original: videos[3], Candidates: []models.Video{videos[2]}},
		},
		NearDuplicateGroups: []CleanupDuplicateGroup{{Original: videos[4], Candidates: []models.Video{videos[5]}}},
		SameSourceGroups:    []CleanupSameSourceGroup{{Preferred: videos[6], Alternative: videos[7]}},
		ClipGroups:          []CleanupClipGroup{{Full: videos[9], Clip: videos[8]}},
	}
	if err := rankCleanupCandidates(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	for _, group := range result.DuplicateGroups {
		if group.Original.Directory != "/a" {
			t.Fatal("exact duplicate groups should be concentrated in /a")
		}
	}
	if result.NearDuplicateGroups[0].Original.ID != videos[5].ID ||
		result.SameSourceGroups[0].Preferred.ID != videos[7].ID ||
		result.SameSourceGroups[0].EstimatedSavings != videos[6].Size ||
		result.ClipGroups[0].Full.ID != videos[9].ID {
		t.Fatal("directory preference changed another category's ranking")
	}
}
