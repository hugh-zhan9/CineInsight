package services

import (
	"testing"
)

func TestLocalLibraryQueryKeepsCompoundConditionsAndUnknownFragments(t *testing.T) {
	parsed, err := parseLocalLibraryQuery("未看 收藏 120分钟以内 人物 张三 标签 科幻")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Conditions) != 5 || len(parsed.Unsupported) != 0 {
		t.Fatalf("compound: %+v", parsed)
	}
	want := map[string]string{"watch": "unwatched", "favorite": "true", "duration": "7200", "person": "张三", "tag": "科幻"}
	for _, condition := range parsed.Conditions {
		if condition.Value != want[condition.Kind] {
			t.Fatal(condition)
		}
	}
	for _, text := range []string{"未看 或 收藏", "轻松的电影", "人物 张三 排除人物 李四", "0分钟以内", "25小时以内"} {
		parsed, err := parseLocalLibraryQuery(text)
		if err != nil {
			t.Fatal(err)
		}
		if len(parsed.Unsupported) == 0 {
			t.Fatalf("silently dropped %q: %+v", text, parsed)
		}
	}
}
func TestLocalLibraryQueryDurationIsExactAndRoleRequiresAcceptance(t *testing.T) {
	parsed, err := parseLocalLibraryQuery("1.5小时以内 导演 张三")
	if err != nil || len(parsed.Conditions) != 2 {
		t.Fatalf("%+v %v", parsed, err)
	}
	var duration, relation bool
	for _, condition := range parsed.Conditions {
		if condition.Kind == "duration" {
			duration = condition.Value == "5400"
		}
		if condition.Kind == "person" {
			relation = condition.RequiresRelationAcceptance
		}
	}
	if !duration || !relation {
		t.Fatal(parsed)
	}
	parsed, err = parseLocalLibraryQuery("标签 收藏")
	if err != nil || len(parsed.Conditions) != 1 || parsed.Conditions[0].Kind != "tag" {
		t.Fatalf("name misread as predicate: %+v %v", parsed, err)
	}
}
func TestExternalLibraryQueryRejectsExecutableUnknownAndInvalidOutput(t *testing.T) {
	text := "未看 收藏 120分钟以内"
	for _, body := range []string{
		`{"conditions":[],"unsupported":[],"sql":"SELECT * FROM videos"}`,
		`{"conditions":[{"kind":"sql","operator":"eq","value":"select","source_text":"未看"}],"unsupported":[]}`,
		`{"conditions":[{"kind":"person","operator":"eq","value":"someone","source_text":"未看","entity_id":1}],"unsupported":[]}`,
		`{"conditions":[{"kind":"duration","operator":"lte","value":"NaN","source_text":"120分钟以内"}],"unsupported":[]}`,
		`{"conditions":[{"kind":"favorite","operator":"eq","value":"false","source_text":"收藏"}],"unsupported":[]}`,
		`{"conditions":[],"unsupported":[]}`,
	} {
		if _, err := parseExternalLibraryQuery(text, body); err == nil {
			t.Fatalf("accepted unsafe/dropped output %s", body)
		}
	}
}

func TestExternalLibraryQueryCannotSwallowOrReverseRecognizedConstraints(t *testing.T) {
	for _, value := range []string{"unwatched", "watched"} {
		body := `{"conditions":[{"kind":"watch","operator":"eq","value":"` + value + `","source_text":"未看 收藏 120分钟以内"}],"unsupported":[]}`
		if _, err := parseExternalLibraryQuery("未看 收藏 120分钟以内", body); err == nil {
			t.Fatal("whole sentence swallowed other conditions")
		}
	}
	body := `{"conditions":[{"kind":"watch","operator":"eq","value":"watched","source_text":"未看"}],"unsupported":[]}`
	if _, err := parseExternalLibraryQuery("未看", body); err == nil {
		t.Fatal("known watch semantics reversed")
	}
}

func TestExternalLibraryQueryCannotSwallowUnknownRequirement(t *testing.T) {
	body := `{"conditions":[{"kind":"watch","operator":"eq","value":"unwatched","source_text":"未看 轻松的电影"}],"unsupported":[]}`
	parsed, err := parseExternalLibraryQuery("未看 轻松的电影", body)
	if err == nil && len(parsed.Unsupported) == 0 {
		t.Fatal("unknown mood requirement disappeared")
	}
}

func TestQueryDurationChineseNumbersAndBothInclusiveBounds(t *testing.T) {
	for text, want := range map[string]string{"两个小时以内": "7200", "一个半小时以内": "5400", "半小时以内": "1800", "一百二十分钟以内": "7200", "不超过两个小时": "7200", "至少二十分钟": "1200"} {
		parsed, err := parseLocalLibraryQuery(text)
		if err != nil || len(parsed.Conditions) != 1 || parsed.Conditions[0].Value != want || len(parsed.Unsupported) != 0 {
			t.Fatalf("%s: %+v %v", text, parsed, err)
		}
		if text == "至少二十分钟" && parsed.Conditions[0].Operator != "gte" {
			t.Fatal(parsed)
		}
	}
}
func TestExternalQueryGroundsLiteralEntitiesAndKeepsUnknownMood(t *testing.T) {
	text := "我想看收藏的科幻电影，不超过两个小时"
	body := `{"conditions":[{"kind":"favorite","operator":"eq","value":"true","source_text":"收藏"},{"kind":"tag","operator":"and","value":"科幻","source_text":"科幻电影"},{"kind":"duration","operator":"lte","value":"7200","source_text":"不超过两个小时"}],"unsupported":[]}`
	parsed, err := parseExternalLibraryQuery(text, body)
	if err != nil || len(parsed.Conditions) != 3 || len(parsed.Unsupported) != 0 {
		t.Fatalf("%+v %v", parsed, err)
	}
	body = `{"conditions":[{"kind":"watch","operator":"eq","value":"unwatched","source_text":"未看 轻松的电影"}],"unsupported":[]}`
	parsed, err = parseExternalLibraryQuery("未看 轻松的电影", body)
	if err != nil || len(parsed.Unsupported) != 1 || parsed.Unsupported[0].SourceText != "轻松的电影" {
		t.Fatalf("lost mood: %+v %v", parsed, err)
	}
}
