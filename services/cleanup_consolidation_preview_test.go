package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func consolidationTestService(t *testing.T, analysis *CleanupAnalysis) (*CleanupService, CleanupConsolidationRequest) {
	t.Helper()
	groups, videos, err := consolidationAnalysisGroups(analysis)
	if err != nil {
		t.Fatal(err)
	}
	analysis.sourceFingerprints = make(map[uint]string)
	for id, video := range videos {
		fingerprint, err := statCleanupFileFingerprint(video.Path)
		if err != nil {
			t.Fatal(err)
		}
		analysis.sourceFingerprints[id] = fingerprint
	}
	service := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}, runID: 7}
	service.SetConsolidationVideoService(&VideoService{})
	request := CleanupConsolidationRequest{}
	for _, group := range groups {
		protection := CleanupConsolidationProtection{Kind: group.kind, MemberIDs: group.members, KeeperID: group.keeper}
		request.Protections = append(request.Protections, protection)
		if group.keeper != 0 {
			request.Groups = append(request.Groups, CleanupConsolidationGroup{Kind: group.kind, MemberIDs: group.members, KeeperID: group.keeper})
		}
	}
	return service, request
}

func consolidationTestFixture(t *testing.T, kind string) (*CleanupService, CleanupConsolidationRequest, models.Video, models.Video, string) {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	a := consolidationTestVideo(t, filepath.Join(root, "source", "a.mp4"), "aaaa")
	b := consolidationTestVideo(t, filepath.Join(root, "target", "b.mp4"), "bbbb")
	analysis := &CleanupAnalysis{}
	switch kind {
	case "exact":
		analysis.DuplicateGroups = []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}}
	case "near":
		analysis.NearDuplicateGroups = []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}}
	case "same-source":
		analysis.SameSourceGroups = []CleanupSameSourceGroup{{RelationID: 1, Preferred: a, Alternative: b}}
	case "clip":
		analysis.ClipGroups = []CleanupClipGroup{{Full: a, Clip: b}}
	}
	service, request := consolidationTestService(t, analysis)
	request.Destination = filepath.Dir(b.Path)
	request.Groups[0].SelectedIDs = []uint{b.ID}
	return service, request, a, b, root
}

func TestCleanupConsolidationPreviewKeepsReviewedVersions(t *testing.T) {
	for _, kind := range []string{"exact", "near", "same-source", "clip"} {
		t.Run(kind, func(t *testing.T) {
			service, request, a, b, root := consolidationTestFixture(t, kind)
			before := consolidationTree(t, root)
			preview, err := service.PreviewConsolidation(context.Background(), request)
			if err != nil || len(preview.Errors) != 0 || preview.PreviewID == "" {
				t.Fatalf("%+v %v", preview, err)
			}
			want := a.ID
			if kind == "exact" {
				want = b.ID
			}
			if preview.Groups[0].KeeperID != want || preview.Items[0].VideoID != want || preview.Items[0].Stay != (kind == "exact") {
				t.Fatalf("目录偏好改变了保留版本: %+v", preview)
			}
			if kind == "exact" && len(preview.Groups[0].SelectedIDs) != 0 {
				t.Fatal("改为保留的等价项还在清理意向中")
			}
			if preview.AnalysisVersion != 7 {
				t.Fatal("未返回分析版本")
			}
			if !reflect.DeepEqual(before, consolidationTree(t, root)) {
				t.Fatal("预览写入了文件")
			}
			var count int64
			if err := database.DB.Model(&models.CleanupConsolidationTask{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("预览不得落任务", err)
			}
			if request.Groups[0].KeeperID != a.ID {
				t.Fatal("预览改变了调用方选择")
			}
		})
	}
}

func TestCleanupConsolidationPreviewManualKeeperAndPrivateToken(t *testing.T) {
	service, request, a, b, _ := consolidationTestFixture(t, "exact")
	request.Groups[0].KeeperPinned = true
	request.Protections[0].KeeperPinned = true
	preview, err := service.PreviewConsolidation(context.Background(), request)
	if err != nil || preview.Groups[0].KeeperID != a.ID || preview.Items[0].Stay {
		t.Fatalf("手工保留不应被目标替换: %+v %v", preview, err)
	}
	firstToken := preview.PreviewID
	preview.Items[0].DestinationPath = "tampered"
	preview.Groups[0].KeeperID = b.ID
	request.Protections[0].MemberIDs[0] = 99999
	if service.consolidationPreview.Preview.Items[0].DestinationPath == "tampered" || service.consolidationPreview.Preview.Groups[0].KeeperID != a.ID || service.consolidationPreview.Protections[0].MemberIDs[0] == 99999 {
		t.Fatal("返回值/输入别名能修改私有确认清单")
	}
	if _, err := service.PreviewConsolidation(context.Background(), request); err == nil {
		t.Fatal("篡改成员应拒绝")
	}
	if service.consolidationPreview != nil {
		t.Fatal("失败的新选择仍留下旧令牌")
	}
	request.Protections[0].MemberIDs[0] = a.ID
	preview, err = service.PreviewConsolidation(context.Background(), request)
	if err != nil || preview.PreviewID == firstToken {
		t.Fatal("新预览应换不可预测令牌", err)
	}
}

func TestCleanupConsolidationPreviewRejectsInvalidAuthority(t *testing.T) {
	cases := []struct {
		name   string
		change func(*CleanupService, *CleanupConsolidationRequest, models.Video, models.Video)
	}{
		{"empty", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) { r.Groups = nil }},
		{"no-analysis", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) { s.status.Analysis = nil }},
		{"stale", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) { s.status.Stale = true }},
		{"running", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) { s.status.Running = true }},
		{"missing-protection", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) { r.Protections = nil }},
		{"duplicate-protection", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Protections = append(r.Protections, r.Protections[0])
		}},
		{"forged-group", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups[0].MemberIDs = []uint{a.ID, 99999}
		}},
		{"partial-group", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups[0].MemberIDs = []uint{a.ID}
		}},
		{"duplicate-group", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups = append(r.Groups, r.Groups[0])
		}},
		{"forged-keeper", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups[0].KeeperID = 99999
			r.Protections[0].KeeperID = 99999
		}},
		{"unpinned-change", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups[0].KeeperID = b.ID
			r.Protections[0].KeeperID = b.ID
			r.Groups[0].SelectedIDs = nil
		}},
		{"pin-mismatch", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups[0].KeeperPinned = true
		}},
		{"skipped", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Protections[0].Skipped = true
		}},
		{"delete-keeper", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups[0].SelectedIDs = []uint{a.ID}
		}},
		{"delete-other", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups[0].SelectedIDs = []uint{99999}
		}},
		{"low-single", func(s *CleanupService, r *CleanupConsolidationRequest, a, b models.Video) {
			r.Groups[0].Kind = "low-duration"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, request, a, b, _ := consolidationTestFixture(t, "near")
			tc.change(service, &request, a, b)
			if _, err := service.PreviewConsolidation(context.Background(), request); err == nil {
				t.Fatal("应拒绝无效授权")
			}
			if service.consolidationPreview != nil {
				t.Fatal("无效授权产生了执行清单")
			}
		})
	}
}

func TestCleanupConsolidationPreviewFixedKeeperCannotSwitch(t *testing.T) {
	for _, kind := range []string{"same-source", "clip"} {
		t.Run(kind, func(t *testing.T) {
			s, request, _, b, _ := consolidationTestFixture(t, kind)
			request.Groups[0].KeeperID = b.ID
			request.Groups[0].KeeperPinned = true
			request.Groups[0].SelectedIDs = nil
			request.Protections[0].KeeperID = b.ID
			request.Protections[0].KeeperPinned = true
			if _, err := s.PreviewConsolidation(context.Background(), request); err == nil {
				t.Fatal("固定保留项不得切换")
			}
		})
	}
}

func TestCleanupConsolidationPreviewRejectsChangedSourceAndDecisions(t *testing.T) {
	for _, change := range []string{"file", "path", "deleted", "dismissed"} {
		t.Run(change, func(t *testing.T) {
			s, request, a, b, _ := consolidationTestFixture(t, "near")
			switch change {
			case "file":
				if err := os.WriteFile(a.Path, []byte("new-content"), 0644); err != nil {
					t.Fatal(err)
				}
			case "path":
				if err := database.DB.Model(&a).Update("path", a.Path+".other").Error; err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := database.DB.Delete(&a).Error; err != nil {
					t.Fatal(err)
				}
			case "dismissed":
				if err := DismissNearDuplicateGroup([]uint{a.ID, b.ID}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.PreviewConsolidation(context.Background(), request); err == nil {
				t.Fatal("失效分析/忽略决定未阻止迁移")
			}
		})
	}
}

func TestCleanupConsolidationPreviewCompleteProtectionAndDeduplicatedKeeper(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	a := consolidationTestVideo(t, filepath.Join(root, "source", "a.mp4"), "a")
	b := consolidationTestVideo(t, filepath.Join(root, "target", "b.mp4"), "b")
	c := consolidationTestVideo(t, filepath.Join(root, "third", "c.mp4"), "c")
	analysis := &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}}, ClipGroups: []CleanupClipGroup{{Full: a, Clip: c}}, LowDuration: []models.Video{b}}
	s, request := consolidationTestService(t, analysis)
	request.Destination = filepath.Dir(b.Path)
	preview, err := s.PreviewConsolidation(context.Background(), request)
	if err != nil || len(preview.Items) != 1 || preview.Items[0].VideoID != a.ID {
		t.Fatalf("跨组选中相同保留项应只移动一次: %+v %v", preview, err)
	}
	if len(s.consolidationPreview.Protections) != 3 || len(s.consolidationPreview.Sources) != 3 {
		t.Fatal("私有计划未保存未参与移动的完整保护")
	}
	// 未纳入组的 keeper 仍跨组保护，删除意向必须裁剪。
	analysis.ClipGroups[0] = CleanupClipGroup{Full: b, Clip: c}
	s, request = consolidationTestService(t, analysis)
	request.Destination = filepath.Dir(c.Path)
	for _, g := range request.Groups {
		if g.Kind == "near" {
			g.SelectedIDs = []uint{b.ID}
			request.Groups = []CleanupConsolidationGroup{g}
			break
		}
	}
	preview, err = s.PreviewConsolidation(context.Background(), request)
	if err != nil || len(preview.Groups[0].SelectedIDs) != 0 {
		t.Fatalf("未纳入组保留项丢失保护: %+v %v", preview, err)
	}
	// 跳过的单条候选在其他组里也不能被移动。
	analysis.LowDuration = []models.Video{a}
	s, request = consolidationTestService(t, analysis)
	request.Destination = filepath.Dir(c.Path)
	for i := range request.Protections {
		if request.Protections[i].Kind == "low-duration" {
			request.Protections[i].Skipped = true
		}
	}
	if _, err := s.PreviewConsolidation(context.Background(), request); err == nil {
		t.Fatal("跨组本组不删冲突未阻止移动")
	}
}

func TestCleanupConsolidationPreviewSuggestedDirectoryAndScanRoots(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	a := consolidationTestVideo(t, filepath.Join(root, "a", "one.mp4"), "large-large")
	b := consolidationTestVideo(t, filepath.Join(root, "b", "one.mp4"), "small")
	c := consolidationTestVideo(t, filepath.Join(root, "a", "two.mp4"), "small")
	d := consolidationTestVideo(t, filepath.Join(root, "b", "two.mp4"), "xx")
	analysis := &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}, {Original: d, Candidates: []models.Video{c}}}}
	s, request := consolidationTestService(t, analysis)
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewConsolidation(context.Background(), request)
	if err != nil || !strings.HasSuffix(preview.Destination, string(filepath.Separator)+"a") || !preview.InScanRoots {
		t.Fatalf("原地组数相同应移动较小的D到甲: %+v %v", preview, err)
	}
	if preview.MoveBytes != 2 {
		t.Fatalf("移动字节量未按保留项计算: %+v", preview)
	}
	for _, g := range preview.Groups {
		if g.Kind != "near" || (g.KeeperID != a.ID && g.KeeperID != d.ID) {
			t.Fatal("目标建议改变近似保留项")
		}
	}
	// Exact 两目录同覆盖、同成本时按路径稳定选择，不能受组顺序影响。
	analysis.NearDuplicateGroups = nil
	analysis.DuplicateGroups = []CleanupDuplicateGroup{{Original: b, Candidates: []models.Video{a}}, {Original: d, Candidates: []models.Video{c}}}
	s, request = consolidationTestService(t, analysis)
	preview, err = s.PreviewConsolidation(context.Background(), request)
	if err != nil || !strings.HasSuffix(preview.Destination, string(filepath.Separator)+"a") || preview.MoveBytes != 0 {
		t.Fatalf("等价副本建议应稳定原地保留: %+v %v", preview, err)
	}
}

func TestCleanupConsolidationPreviewLargeBatchAndJSONSnapshot(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	analysis := &CleanupAnalysis{}
	const groupCount = 260 // 520 成员跨越 SQLite 单次查询的内部批边界。
	for i := 0; i < groupCount; i++ {
		a := consolidationTestVideo(t, filepath.Join(root, "a", fmt.Sprintf("%04d.mp4", i)), "a")
		b := consolidationTestVideo(t, filepath.Join(root, "b", fmt.Sprintf("%04d.mp4", i)), "b")
		analysis.DuplicateGroups = append(analysis.DuplicateGroups, CleanupDuplicateGroup{Original: a, Candidates: []models.Video{b}})
	}
	s, request := consolidationTestService(t, analysis)
	preview, err := s.PreviewConsolidation(context.Background(), request)
	if err != nil || len(preview.Items) != groupCount || preview.MoveBytes != 0 {
		t.Fatalf("大批预览失败: %v", err)
	}
	data, err := json.Marshal(s.consolidationPreview)
	if err != nil {
		t.Fatal(err)
	}
	var restored cleanupConsolidationPlan
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.SchemaVersion != 1 || len(restored.Sources) != groupCount*2 || len(restored.Protections) != groupCount {
		t.Fatal("持久快照丢失版本或完整保护")
	}
	for _, source := range restored.Sources {
		if source.File.Identity == "" || source.File.ModTimeNS == 0 {
			t.Fatal("恢复快照未保留源身份/版本")
		}
	}
}

func TestCleanupConsolidationPreviewRequiresUnselectedProtections(t *testing.T) {
	s, request, _, b, _ := consolidationTestFixture(t, "near")
	s.status.Analysis.LowDuration = []models.Video{b}
	// 所选重复组完全有效，但漏掉了未纳入整理的单条候选保护。
	if _, err := s.PreviewConsolidation(context.Background(), request); err == nil {
		t.Fatal("未纳入整理的组也必须存在于保护快照")
	}
}

func TestCleanupConsolidationPreviewNearManualKeeperRemainsSelected(t *testing.T) {
	s, request, _, b, _ := consolidationTestFixture(t, "near")
	request.Groups[0].KeeperID = b.ID
	request.Groups[0].KeeperPinned = true
	request.Groups[0].SelectedIDs = nil
	request.Protections[0].KeeperID = b.ID
	request.Protections[0].KeeperPinned = true
	preview, err := s.PreviewConsolidation(context.Background(), request)
	if err != nil || preview.Groups[0].KeeperID != b.ID || !preview.Items[0].Stay {
		t.Fatalf("近似重复的手动保留项未保持: %+v %v", preview, err)
	}
}

func TestCleanupConsolidationPreviewRejectsAliasSourceConflict(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	a := consolidationTestVideo(t, filepath.Join(root, "source", "film.mp4"), "movie")
	b := consolidationTestVideo(t, filepath.Join(root, "target", "b.mp4"), "b")
	c := consolidationTestVideo(t, filepath.Join(root, "target", "c.mp4"), "c")
	aliasDir := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Dir(a.Path), aliasDir); err != nil {
		t.Fatal(err)
	}
	alias := a
	alias.ID = 0
	alias.Path = filepath.Join(aliasDir, "film.mp4")
	alias.Directory = aliasDir
	if err := database.DB.Create(&alias).Error; err != nil {
		t.Fatal(err)
	}
	analysis := &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{
		{Original: a, Candidates: []models.Video{b}},
		{Original: alias, Candidates: []models.Video{c}},
	}}
	s, request := consolidationTestService(t, analysis)
	request.Destination = filepath.Dir(b.Path)
	if _, err := s.PreviewConsolidation(context.Background(), request); err == nil {
		t.Fatal("同一实际来源不能被两个库记录重复移动")
	}
}

func TestCleanupConsolidationPreviewBlocksOtherActiveSourceAliases(t *testing.T) {
	for _, mode := range []string{"skipped", "outside-analysis", "file-symlink"} {
		t.Run(mode, func(t *testing.T) {
			s, request, a, _, root := consolidationTestFixture(t, "near")
			aliasDir := filepath.Join(root, "alias")
			var aliasPath string
			if mode == "file-symlink" {
				aliasPath = filepath.Join(root, "file-link.mp4")
				if err := os.Symlink(a.Path, aliasPath); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink(filepath.Dir(a.Path), aliasDir); err != nil {
					t.Fatal(err)
				}
				aliasPath = filepath.Join(aliasDir, filepath.Base(a.Path))
			}
			alias := models.Video{Path: aliasPath, Directory: filepath.Dir(aliasPath)}
			if err := database.DB.Create(&alias).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "skipped" {
				s.status.Analysis.LowDuration = []models.Video{alias}
				s.status.Analysis.sourceFingerprints[alias.ID] = s.status.Analysis.sourceFingerprints[a.ID]
				request.Protections = append(request.Protections, CleanupConsolidationProtection{Kind: "low-duration", MemberIDs: []uint{alias.ID}, Skipped: true})
			}
			preview, err := s.PreviewConsolidation(context.Background(), request)
			if err == nil && (preview.PreviewID != "" || len(preview.Errors) == 0) {
				t.Fatalf("移动会破坏另一个活跃视频 ID=%d 的来源，应明确阻塞: %+v", alias.ID, preview)
			}
			if s.consolidationPreview != nil {
				t.Fatal("共享源路径产生了执行令牌")
			}
			var loaded models.Video
			if err := database.DB.First(&loaded, alias.ID).Error; err != nil || loaded.Path != alias.Path {
				t.Fatal("预览不应重写未授权记录", err)
			}
		})
	}
}

// 朴素 oracle 故意逐目录、逐组重算最终保留集合，只用于随机小规模对照。
func consolidationSuggestionOracle(groups []CleanupConsolidationGroup, sources map[uint]cleanupConsolidationVideoSource, sizes map[uint]int64) string {
	directories := make(map[string]string)
	for _, group := range groups {
		for _, id := range group.MemberIDs {
			source := sources[id].File
			directories[filepath.Dir(source.RealPath)] = strings.Split(source.Identity, ":")[0]
		}
	}
	best := ""
	var bestStays int
	var bestTotal, bestCross int64
	for dir, device := range directories {
		stays := 0
		keepers := make(map[uint]bool)
		for _, group := range groups {
			keeper := group.KeeperID
			if group.Kind == "exact" && !group.KeeperPinned && filepath.Dir(sources[keeper].File.RealPath) != dir {
				var chosen uint
				for _, id := range group.MemberIDs {
					if filepath.Dir(sources[id].File.RealPath) == dir && (chosen == 0 || id < chosen) {
						chosen = id
					}
				}
				if chosen != 0 {
					keeper = chosen
				}
			}
			keepers[keeper] = true
			if filepath.Dir(sources[keeper].File.RealPath) == dir {
				stays++
			}
		}
		var moving, cross int64
		for id := range keepers {
			source := sources[id].File
			if filepath.Dir(source.RealPath) == dir {
				continue
			}
			moving += sizes[id]
			if strings.Split(source.Identity, ":")[0] != device {
				cross += sizes[id]
			}
		}
		if best == "" || stays > bestStays || (stays == bestStays && (cross < bestCross || (cross == bestCross && (moving < bestTotal || (moving == bestTotal && dir < best))))) {
			best, bestStays, bestTotal, bestCross = dir, stays, moving, cross
		}
	}
	return best
}

func TestConsolidationPreviewSuggestionMatchesOracle(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	sources := make(map[uint]cleanupConsolidationVideoSource)
	sizes := make(map[uint]int64)
	const videoCount = 16
	for i := 0; i < videoCount; i++ {
		id := uint(i + 1)
		dir := filepath.Join(root, fmt.Sprintf("dir-%d", i%4))
		path := filepath.Join(dir, fmt.Sprintf("film-%d.mp4", i))
		mustCreateFile(t, path)
		size := int64(7*i + 1)
		sources[id] = cleanupConsolidationVideoSource{File: FileMigrationSource{RealPath: path, Identity: fmt.Sprintf("%d:%d", i%2+1, id), Size: size}}
		if i%3 == 0 {
			mustCreateFile(t, strings.TrimSuffix(path, ".mp4")+".srt")
			size++
		}
		sizes[id] = size
	}
	random := rand.New(rand.NewSource(8048))
	for attempt := 0; attempt < 250; attempt++ {
		groupCount := random.Intn(40) + 1
		groups := make([]CleanupConsolidationGroup, 0, groupCount)
		for i := 0; i < groupCount; i++ {
			permutation := random.Perm(videoCount)
			members := make([]uint, random.Intn(4)+2)
			for j := range members {
				members[j] = uint(permutation[j] + 1)
			}
			kind := []string{"exact", "near", "same-source", "clip"}[random.Intn(4)]
			groups = append(groups, CleanupConsolidationGroup{Kind: kind, MemberIDs: members, KeeperID: members[0], KeeperPinned: random.Intn(3) == 0})
		}
		want := consolidationSuggestionOracle(groups, sources, sizes)
		got, err := suggestedConsolidationDestination(context.Background(), groups, sources)
		if err != nil || got != want {
			t.Fatalf("case %d got=%s want=%s err=%v groups=%+v", attempt, got, want, err, groups)
		}
	}
}

type consolidationCountingContext struct {
	context.Context
	calls int
}

func (c *consolidationCountingContext) Err() error { c.calls++; return c.Context.Err() }

func TestConsolidationPreviewSuggestionTenThousandTiedDirectories(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	const count = 10000
	groups := make([]CleanupConsolidationGroup, 0, count)
	sources := make(map[uint]cleanupConsolidationVideoSource, count*2)
	for i := 0; i < count; i++ {
		dir := filepath.Join(root, fmt.Sprintf("%05d", i))
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		id := uint(2*i + 1)
		groups = append(groups, CleanupConsolidationGroup{Kind: "near", MemberIDs: []uint{id, id + 1}, KeeperID: id})
		sources[id] = cleanupConsolidationVideoSource{File: FileMigrationSource{RealPath: filepath.Join(dir, "film.mp4"), Identity: "1:1", Size: 1}}
		sources[id+1] = cleanupConsolidationVideoSource{File: FileMigrationSource{RealPath: filepath.Join(dir, "other.mp4"), Identity: "1:2", Size: 1}}
	}
	ctx := &consolidationCountingContext{Context: context.Background()}
	start := time.Now()
	got, err := suggestedConsolidationDestination(ctx, groups, sources)
	elapsed := time.Since(start)
	if err != nil || got != filepath.Join(root, "00000") {
		t.Fatalf("并列目录建议未稳定选择: %s %v", got, err)
	}
	t.Logf("%d dirs/groups: %s; context checks=%d", count, elapsed, ctx.calls)
	// 不钉墙钟时间；可观察的工作步数必须远小于目录×组，防止批量又回到平方遍历。
	if ctx.calls > count*20 {
		t.Fatalf("目录建议仍在按目录×组重复评估: %d checks", ctx.calls)
	}
}
