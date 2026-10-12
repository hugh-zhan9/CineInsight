package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 本地规则自动化（D-RA-01..05）的服务测试。

func ptrFloat(v float64) *float64 { return &v }
func ptrBool(v bool) *bool        { return &v }

func autoTag(t *testing.T, name string) models.Tag {
	t.Helper()
	tag := models.Tag{Name: name}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	return tag
}

func autoVideo(t *testing.T, path string, mutate func(*models.Video)) models.Video {
	t.Helper()
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: 1000, Duration: 600, Height: 1080, Width: 1920}
	if mutate != nil {
		mutate(&video)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

func saveAutoRule(t *testing.T, svc *AutomationRuleService, input AutomationRuleInput) AutomationRuleView {
	t.Helper()
	if input.Name == "" {
		input.Name = "测试规则"
	}
	if input.MediaKind == "" {
		input.MediaKind = AutomationMediaVideo
	}
	view, err := svc.SaveAutomationRule(input)
	if err != nil {
		t.Fatalf("保存规则失败: %v", err)
	}
	return view
}

func videoHasTag(t *testing.T, videoID, tagID uint) bool {
	t.Helper()
	var count int64
	if err := database.DB.Table("video_tags").Where("video_id = ? AND tag_id = ?", videoID, tagID).Count(&count).Error; err != nil {
		t.Fatalf("查询视频标签失败: %v", err)
	}
	return count == 1
}

func ruleWrites(t *testing.T, ruleID uint) []models.AutomationRuleWrite {
	t.Helper()
	var rows []models.AutomationRuleWrite
	if err := database.DB.Where("rule_id = ?", ruleID).Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("读取规则账本失败: %v", err)
	}
	return rows
}

func evaluateVideos(t *testing.T, svc *AutomationRuleService, ids ...uint) {
	t.Helper()
	if err := svc.evaluateNewMedia(context.Background(), AutomationMediaVideo, ids); err != nil {
		t.Fatalf("评估失败: %v", err)
	}
}

func invalidField(t *testing.T, err error) string {
	t.Helper()
	var validation *AutomationRuleValidationError
	if !errors.As(err, &validation) || !errors.Is(err, ErrAutomationRuleInvalid) {
		t.Fatalf("应报 invalid_rule，实际 %v", err)
	}
	if !strings.HasPrefix(err.Error(), "invalid_rule: ") {
		t.Fatalf("错误文案应以 invalid_rule 开头: %q", err.Error())
	}
	return validation.Field
}

func TestAutomationRuleSaveValidation(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "待整理")
	automatic := models.Tag{Name: "短视频", AutomaticKind: shortVideoAutomaticTagKind}
	if err := database.DB.Create(&automatic).Error; err != nil {
		t.Fatal(err)
	}
	collection := models.MediaCollection{Name: "合集", NormalizedName: "合集"}
	if err := database.DB.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	dir := AutomationRuleCondition{Type: automationCondUnderDirectory, Path: "/lib/下载"}
	addTag := AutomationRuleAction{Type: automationActionAddTag, TagID: tag.ID}
	cases := []struct {
		name  string
		input AutomationRuleInput
		field string
	}{
		{"条件为空", AutomationRuleInput{Name: "r", MediaKind: "video", Actions: []AutomationRuleAction{addTag}}, "conditions"},
		{"图片不能用时长", AutomationRuleInput{Name: "r", MediaKind: "image", Conditions: []AutomationRuleCondition{{Type: automationCondDuration, Min: ptrFloat(1)}}, Actions: []AutomationRuleAction{addTag}}, "conditions[0].type"},
		{"视频不能用地理位置", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{{Type: automationCondHasGPS, Value: ptrBool(true)}}, Actions: []AutomationRuleAction{addTag}}, "conditions[0].type"},
		{"正则超长", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{{Type: automationCondNameRegex, Pattern: strings.Repeat("a", 201)}}, Actions: []AutomationRuleAction{addTag}}, "conditions[0].pattern"},
		{"正则写错", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{dir, {Type: automationCondNameRegex, Pattern: "(["}}, Actions: []AutomationRuleAction{addTag}}, "conditions[1].pattern"},
		{"通配符写错", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{{Type: automationCondNameGlob, Pattern: "[a"}}, Actions: []AutomationRuleAction{addTag}}, "conditions[0].pattern"},
		{"目录不是绝对路径", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: "相对"}}, Actions: []AutomationRuleAction{addTag}}, "conditions[0].path"},
		{"区间两端都空", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{{Type: automationCondSizeBetween}}, Actions: []AutomationRuleAction{addTag}}, "conditions[0].min"},
		{"区间颠倒", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{{Type: automationCondHeight, Min: ptrFloat(2000), Max: ptrFloat(1000)}}, Actions: []AutomationRuleAction{addTag}}, "conditions[0].max"},
		{"未知条件", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{{Type: "rating_above"}}, Actions: []AutomationRuleAction{addTag}}, "conditions[0].type"},
		{"没有动作", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{dir}}, "actions"},
		{"图片不能加入作品集", AutomationRuleInput{Name: "r", MediaKind: "image", Conditions: []AutomationRuleCondition{dir}, Actions: []AutomationRuleAction{{Type: automationActionAddToCollection, CollectionID: collection.ID}}}, "actions[0].type"},
		{"标签不存在", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{dir}, Actions: []AutomationRuleAction{{Type: automationActionAddTag, TagID: 9999}}}, "actions[0].tag_id"},
		{"自动标签不能用", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{dir}, Actions: []AutomationRuleAction{{Type: automationActionAddTag, TagID: automatic.ID}}}, "actions[0].tag_id"},
		{"作品集不存在", AutomationRuleInput{Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{dir}, Actions: []AutomationRuleAction{{Type: automationActionAddToCollection, CollectionID: 9999}}}, "actions[0].collection_id"},
		{"名称为空", AutomationRuleInput{Name: "  ", MediaKind: "video", Conditions: []AutomationRuleCondition{dir}, Actions: []AutomationRuleAction{addTag}}, "name"},
	}
	for _, tc := range cases {
		_, err := svc.SaveAutomationRule(tc.input)
		if field := invalidField(t, err); field != tc.field {
			t.Fatalf("%s: 字段=%q want %q (err=%v)", tc.name, field, tc.field, err)
		}
	}
	var count int64
	database.DB.Model(&models.AutomationRule{}).Count(&count)
	if count != 0 {
		t.Fatalf("校验失败的规则不应落库: %d", count)
	}
	if _, err := svc.SaveAutomationRule(AutomationRuleInput{ID: 4242, Name: "r", MediaKind: "video", Conditions: []AutomationRuleCondition{dir}, Actions: []AutomationRuleAction{addTag}}); !errors.Is(err, ErrAutomationRuleNotFound) {
		t.Fatalf("更新不存在的规则应报 rule_not_found: %v", err)
	}
}

// 每种条件的求值，含未知值（时长/高度为 0、没有视频流、EXIF 未解析）一律不满足。
func TestAutomationRuleConditionEvaluation(t *testing.T) {
	video := automationMediaFacts{Kind: AutomationMediaVideo, ID: 1, Path: "/lib/下载/Movie.Name.2024.MKV", Directory: "/lib/下载", Size: 2048, Duration: 5400, Height: 2160, Codec: "hevc"}
	unknown := automationMediaFacts{Kind: AutomationMediaVideo, ID: 2, Path: "/lib/下载/raw.mp4", Directory: "/lib/下载", Size: 10}
	now := time.Now()
	withGPS := imageAutomationFacts(models.Image{ID: 3, Path: "/pics/a.jpg", ExifParsedAt: &now, GPSLatitude: ptrFloat(1), GPSLongitude: ptrFloat(2)})
	noGPS := imageAutomationFacts(models.Image{ID: 4, Path: "/pics/b.jpg", ExifParsedAt: &now})
	unparsed := imageAutomationFacts(models.Image{ID: 5, Path: "/pics/c.jpg", GPSLatitude: ptrFloat(1), GPSLongitude: ptrFloat(2)})
	cases := []struct {
		name  string
		cond  AutomationRuleCondition
		facts automationMediaFacts
		want  bool
	}{
		{"目录：直接在下面", AutomationRuleCondition{Type: automationCondUnderDirectory, Path: "/lib/下载"}, video, true},
		{"目录：上层目录也算", AutomationRuleCondition{Type: automationCondUnderDirectory, Path: "/lib"}, video, true},
		{"目录：按路径段比较", AutomationRuleCondition{Type: automationCondUnderDirectory, Path: "/lib/下"}, video, false},
		{"目录：别处", AutomationRuleCondition{Type: automationCondUnderDirectory, Path: "/other"}, video, false},
		{"包含：不分大小写", AutomationRuleCondition{Type: automationCondNameContains, Text: "movie.name"}, video, true},
		{"包含：只看文件名", AutomationRuleCondition{Type: automationCondNameContains, Text: "下载"}, video, false},
		{"通配", AutomationRuleCondition{Type: automationCondNameGlob, Pattern: "Movie.*.MKV"}, video, true},
		{"通配：只匹配文件名", AutomationRuleCondition{Type: automationCondNameGlob, Pattern: "/lib/*"}, video, false},
		{"正则", AutomationRuleCondition{Type: automationCondNameRegex, Pattern: `\.20\d\d\.`}, video, true},
		{"正则不中", AutomationRuleCondition{Type: automationCondNameRegex, Pattern: `^raw`}, video, false},
		{"扩展名：小写比较", AutomationRuleCondition{Type: automationCondExtensionIn, Exts: []string{"mkv"}}, video, true},
		{"扩展名不中", AutomationRuleCondition{Type: automationCondExtensionIn, Exts: []string{"mp4"}}, video, false},
		{"大小：闭区间下端", AutomationRuleCondition{Type: automationCondSizeBetween, Min: ptrFloat(2048)}, video, true},
		{"大小：闭区间上端", AutomationRuleCondition{Type: automationCondSizeBetween, Max: ptrFloat(2048)}, video, true},
		{"大小：超出上限", AutomationRuleCondition{Type: automationCondSizeBetween, Max: ptrFloat(2047)}, video, false},
		{"时长", AutomationRuleCondition{Type: automationCondDuration, Min: ptrFloat(3600), Max: ptrFloat(7200)}, video, true},
		{"时长：0 视为未知", AutomationRuleCondition{Type: automationCondDuration, Max: ptrFloat(7200)}, unknown, false},
		{"高度", AutomationRuleCondition{Type: automationCondHeight, Min: ptrFloat(2160)}, video, true},
		{"高度：不够", AutomationRuleCondition{Type: automationCondHeight, Min: ptrFloat(2161)}, video, false},
		{"高度：0 视为未知", AutomationRuleCondition{Type: automationCondHeight, Max: ptrFloat(5000)}, unknown, false},
		{"编码", AutomationRuleCondition{Type: automationCondVideoCodecIn, Codecs: []string{"h264", "hevc"}}, video, true},
		{"编码：没有流视为未知", AutomationRuleCondition{Type: automationCondVideoCodecIn, Codecs: []string{"hevc"}}, unknown, false},
		{"有地理位置", AutomationRuleCondition{Type: automationCondHasGPS, Value: ptrBool(true)}, withGPS, true},
		{"没有地理位置", AutomationRuleCondition{Type: automationCondHasGPS, Value: ptrBool(false)}, noGPS, true},
		{"有地理位置 vs 没有", AutomationRuleCondition{Type: automationCondHasGPS, Value: ptrBool(true)}, noGPS, false},
		{"EXIF 未解析视为未知（要有）", AutomationRuleCondition{Type: automationCondHasGPS, Value: ptrBool(true)}, unparsed, false},
		{"EXIF 未解析视为未知（要没有）", AutomationRuleCondition{Type: automationCondHasGPS, Value: ptrBool(false)}, unparsed, false},
	}
	for _, tc := range cases {
		rule, err := compileAutomationRule(models.AutomationRule{
			ID: 1, Name: "r", MediaKind: tc.facts.Kind, ConditionsJSON: mustAutomationJSON(t, []AutomationRuleCondition{tc.cond}),
			ActionsJSON: `[{"type":"add_tag","tag_id":1}]`,
		})
		if err != nil {
			t.Fatalf("%s: 编译失败 %v", tc.name, err)
		}
		if got := rule.matches(tc.facts); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func mustAutomationJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := encodeAutomationJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// 列表、排序、启停、删除：删除规则只删它的账本，不撤它加过的标签（D-RA-03）。
func TestAutomationRuleListReorderEnableDelete(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "待整理")
	root := t.TempDir()
	video := autoVideo(t, filepath.Join(root, "a.mp4"), nil)
	cond := []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root + "/"}}
	actions := []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}, {Type: automationActionAddTag, TagID: tag.ID}}
	first := saveAutoRule(t, svc, AutomationRuleInput{Name: " 第一 ", Enabled: true, Conditions: cond, Actions: actions})
	second := saveAutoRule(t, svc, AutomationRuleInput{Name: "第二", Enabled: false, Conditions: cond, Actions: actions})
	if first.Name != "第一" || first.Position != 1 || second.Position != 2 || second.Enabled {
		t.Fatalf("新建规则排在最后、名称去空白、启用按入参写: %+v %+v", first, second)
	}
	if len(first.Actions) != 1 || first.Conditions[0].Path != root {
		t.Fatalf("重复动作去重、目录规整: %+v", first)
	}
	if err := svc.ReorderAutomationRules([]uint{second.ID, first.ID}); err != nil {
		t.Fatal(err)
	}
	views, err := svc.ListAutomationRules()
	if err != nil || len(views) != 2 || views[0].ID != second.ID || views[1].ID != first.ID {
		t.Fatalf("排序后列表顺序错误: %+v err=%v", views, err)
	}
	if err := svc.ReorderAutomationRules([]uint{first.ID}); invalidField(t, err) != "ids" {
		t.Fatalf("排序必须给出全部规则: %v", err)
	}
	if view, err := svc.SetAutomationRuleEnabled(second.ID, true); err != nil || !view.Enabled {
		t.Fatalf("启用失败: %+v err=%v", view, err)
	}
	if _, err := svc.SetAutomationRuleEnabled(9999, true); !errors.Is(err, ErrAutomationRuleNotFound) {
		t.Fatalf("启停不存在的规则应报 rule_not_found: %v", err)
	}
	updated, err := svc.SaveAutomationRule(AutomationRuleInput{ID: first.ID, Name: "改名", MediaKind: AutomationMediaVideo, Enabled: false, Conditions: cond, Actions: actions})
	if err != nil || updated.Name != "改名" || updated.Enabled || updated.Position != 2 {
		t.Fatalf("更新应保留位置: %+v err=%v", updated, err)
	}
	if _, err := svc.SaveAutomationRule(AutomationRuleInput{ID: first.ID, Name: "改名", MediaKind: AutomationMediaImage, Conditions: cond, Actions: actions}); invalidField(t, err) != "media_kind" {
		t.Fatalf("已保存的规则不能换媒体类型: %v", err)
	}
	evaluateVideos(t, svc, video.ID)
	if !videoHasTag(t, video.ID, tag.ID) || len(ruleWrites(t, second.ID)) != 1 {
		t.Fatal("启用的规则应生效")
	}
	if err := svc.DeleteAutomationRule(second.ID); err != nil {
		t.Fatal(err)
	}
	if len(ruleWrites(t, second.ID)) != 0 || !videoHasTag(t, video.ID, tag.ID) {
		t.Fatal("删除规则只删账本，不撤标签")
	}
	if err := svc.DeleteAutomationRule(second.ID); !errors.Is(err, ErrAutomationRuleNotFound) {
		t.Fatalf("重复删除应报 rule_not_found: %v", err)
	}
}

// 图片规则：has_gps 在 EXIF 未解析时不满足；新建图片经观察者入队后自动评估。
func TestAutomationRuleImageGPSAndNewImageObserver(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "有定位")
	root := t.TempDir()
	now := time.Now()
	located := models.Image{Name: "a.jpg", Path: filepath.Join(root, "a.jpg"), Directory: root, Size: 1, ExifParsedAt: &now, GPSLatitude: ptrFloat(30), GPSLongitude: ptrFloat(120)}
	unparsed := models.Image{Name: "b.jpg", Path: filepath.Join(root, "b.jpg"), Directory: root, Size: 1, GPSLatitude: ptrFloat(30), GPSLongitude: ptrFloat(120)}
	database.DB.Create(&located)
	database.DB.Create(&unparsed)
	saveAutoRule(t, svc, AutomationRuleInput{MediaKind: AutomationMediaImage, Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondHasGPS, Value: ptrBool(true)}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	images := NewImageService()
	var observed []uint
	images.SetNewImageObserver(func(id uint) {
		observed = append(observed, id)
		svc.EnqueueImage(id)
	})
	newPath := filepath.Join(root, "c.png")
	mustCreateFile(t, newPath)
	created, err := images.addImage(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[0] != created.ID {
		t.Fatalf("新建图片应入队一次: %v", observed)
	}
	svc.EnqueueImage(located.ID)
	svc.EnqueueImage(unparsed.ID)
	waitAutomationQueueIdle(t, svc)
	var tagged []uint
	database.DB.Table("image_tags").Where("tag_id = ?", tag.ID).Order("image_id").Pluck("image_id", &tagged)
	if len(tagged) != 1 || tagged[0] != located.ID {
		t.Fatalf("只有已解析且有定位的图片应加标签: %v (new=%d)", tagged, created.ID)
	}
	var writes int64
	database.DB.Model(&models.AutomationRuleWrite{}).Where("media_kind = ?", AutomationMediaImage).Count(&writes)
	if writes != 1 {
		t.Fatalf("未解析 EXIF 的图片不应写账本: %d", writes)
	}
}
