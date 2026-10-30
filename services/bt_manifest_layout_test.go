package services

import (
	"strings"
	"testing"

	"video-master/services/btmanifest"
)

func TestBTOriginalLayoutV2RulesAndStableIDs(t *testing.T) {
	for _, test := range []struct {
		name        string
		paths, want []string
	}{
		{"display", []string{"one.mkv"}, []string{"one.mkv"}},
		{"display", []string{"dir/one.mkv"}, []string{"display/dir/one.mkv"}},
		{"display", []string{"one.mkv", "two.mkv"}, []string{"display/one.mkv", "display/two.mkv"}},
		{"display", []string{"display/one.mkv", "display/two.mkv"}, []string{"display/display/one.mkv", "display/display/two.mkv"}},
		{"/display-only", []string{"one.mkv"}, []string{"one.mkv"}},
	} {
		t.Run(strings.Join(test.paths, ","), func(t *testing.T) {
			m := btmanifest.Manifest{Name: test.name, InfoHashV2: strings.Repeat("2", 64), Digest: "digest"}
			for i, path := range test.paths {
				m.Files = append(m.Files, btmanifest.File{ID: strings.Repeat(string(rune('a'+i)), 64), Path: path, Size: 1, Selectable: true})
			}
			mapped, err := btOriginalLayout(m)
			if err != nil {
				t.Fatal(err)
			}
			for i, file := range mapped.Files {
				if file.Path != test.want[i] || file.ID != m.Files[i].ID || m.Files[i].Path != test.paths[i] {
					t.Fatalf("wrong layout or changed manifest: %+v", mapped)
				}
			}
			if mapped.Digest != m.Digest || mapped.InfoHashV2 != m.InfoHashV2 {
				t.Fatal("layout changed identity")
			}
		})
	}
}
func TestBTOriginalLayoutRejectsSanitizationAndExpansion(t *testing.T) {
	for _, name := range []string{"../escape", "..", "a/b", "a\\b", "a\x00b", "a\nb", "a\u202eb", strings.Repeat("x", 241)} {
		m := btmanifest.Manifest{Name: name, InfoHashV2: strings.Repeat("2", 64), Files: []btmanifest.File{{Path: "a"}, {Path: "b"}}}
		if _, err := btOriginalLayout(m); err == nil {
			t.Fatal("unsafe client root accepted")
		}
	}
	m := btmanifest.Manifest{Name: "root", InfoHashV2: strings.Repeat("2", 64), Files: []btmanifest.File{{Path: strings.Repeat("d/", 63) + "a"}}}
	if _, err := btOriginalLayout(m); err == nil {
		t.Fatal("client root exceeded depth budget")
	}
	m.Files = make([]btmanifest.File, 10000)
	path := strings.Repeat(strings.Repeat("a", 200)+"/", 9) + "a"
	for i := range m.Files {
		m.Files[i].Path = path
	}
	if _, err := btOriginalLayout(m); err == nil {
		t.Fatal("expanded path budget not checked")
	}
}
func TestBTOriginalLayoutDoesNotPrefixV1OrHybrid(t *testing.T) {
	m := btmanifest.Manifest{Name: "season", InfoHashV1: strings.Repeat("1", 40), InfoHashV2: strings.Repeat("2", 64), Files: []btmanifest.File{{ID: "real", Path: "season/one.mkv", Selectable: true}, {ID: "pad", Padding: true}}}
	mapped, err := btOriginalLayout(m)
	if err != nil || mapped.Files[0].Path != "season/one.mkv" || mapped.Files[1].Path != "" {
		t.Fatal("hybrid prefixed or padding fabricated", err)
	}
}
