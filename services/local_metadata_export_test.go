package services

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type exportedMovieNFO struct {
	Title      string   `xml:"title"`
	UserRating string   `xml:"userrating"`
	Tags       []string `xml:"tag"`
	Actors     []struct {
		Name  string `xml:"name"`
		Role  string `xml:"role"`
		Thumb string `xml:"thumb"`
	} `xml:"actor"`
	Set struct {
		Name     string `xml:"name"`
		Overview string `xml:"overview"`
	} `xml:"set"`
	OriginalTitle string `xml:"originaltitle"`
	Plot          string `xml:"plot"`
	UniqueID      struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
	FileInfo struct {
		Width int `xml:"streamdetails>video>width"`
	} `xml:"fileinfo"`
}

// 应用自身解析器（parseLocalMovieNFO）的往返只覆盖 title/actor/set；
// 导出的 tag/userrating 仅供第三方消费（Kodi/Jellyfin），下方用通用
// XML 解码单独校验这两个字段。
func TestExportLocalMetadataNFORoundTripsThroughCurrentParser(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	rating := 8.5
	result, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{
		DisplayTitle:   `A & <B> "特别版"`,
		PersonalRating: &rating,
		Tags:           []string{" 剧情 & 悬疑 ", "剧情 & 悬疑", "收藏 <精选>"},
		People:         []string{"Alice & Bob", "张三 <主演>"},
		Collection:     "系列 & <一>",
	})
	if err != nil {
		t.Fatalf("export NFO: %v", err)
	}
	if !result.Created || result.NFOPath != filepath.Join(root, "movie.nfo") || result.Size <= 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	content, err := os.ReadFile(result.NFOPath)
	if err != nil {
		t.Fatalf("read NFO: %v", err)
	}
	document, err := parseLocalMovieNFO(content)
	if err != nil {
		t.Fatalf("current parser cannot read exported NFO: %v\n%s", err, content)
	}
	if document.Title != `A & <B> "特别版"` || document.Collection != "系列 & <一>" {
		t.Fatalf("round-trip scalar fields: %#v", document)
	}
	if len(document.People) != 2 || document.People[0] != "Alice & Bob" || document.People[1] != "张三 <主演>" {
		t.Fatalf("round-trip people: %#v", document.People)
	}
	var kodi exportedMovieNFO
	if err := xml.Unmarshal(content, &kodi); err != nil {
		t.Fatalf("decode Kodi fields: %v", err)
	}
	if kodi.UserRating != "8.5" || len(kodi.Tags) != 2 || kodi.Tags[0] != "剧情 & 悬疑" || kodi.Tags[1] != "收藏 <精选>" {
		t.Fatalf("Kodi rating/tags: %#v", kodi)
	}
	if matches, err := filepath.Glob(filepath.Join(root, ".movie.nfo.tmp-*")); err != nil || len(matches) != 0 {
		t.Fatalf("atomic export left temporary files: matches=%v err=%v", matches, err)
	}
}

func TestExportLocalMetadataNFOMergesManagedFieldsAndPreservesUnknownXML(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mkv")
	nfoPath := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	existing := `<?xml version="1.0"?>
<movie data-owner="third-party">
  <title>Old title</title><originaltitle>Original title</originaltitle><plot>Keep plot</plot>
  <uniqueid type="imdb">tt1234567</uniqueid><tag>Old tag</tag>
  <actor><name>Alice</name><role>Lead</role><thumb>alice.jpg</thumb></actor>
  <actor><name>Removed actor</name><role>Removed</role></actor>
  <set><name>Old set</name><overview>Keep overview</overview></set>
  <fileinfo><streamdetails><video><width>1920</width></video></streamdetails></fileinfo>
</movie>`
	if err := os.WriteFile(nfoPath, []byte(existing), 0600); err != nil {
		t.Fatalf("write existing NFO: %v", err)
	}
	rating := 7.5
	result, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{
		DisplayTitle:   "New title",
		PersonalRating: &rating,
		Tags:           []string{"New tag"},
		People:         []string{"Alice", "Bob"},
		Collection:     "New set",
	})
	if err != nil {
		t.Fatalf("merge NFO: %v", err)
	}
	if result.Created {
		t.Fatalf("merge should report existing NFO")
	}
	content, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatalf("read merged NFO: %v", err)
	}
	document, err := parseLocalMovieNFO(content)
	if err != nil {
		t.Fatalf("current parser cannot read merged NFO: %v", err)
	}
	if document.Title != "New title" || document.Collection != "New set" || len(document.People) != 2 {
		t.Fatalf("merged parser document: %#v", document)
	}
	var kodi exportedMovieNFO
	if err := xml.Unmarshal(content, &kodi); err != nil {
		t.Fatalf("decode merged NFO: %v", err)
	}
	if kodi.OriginalTitle != "Original title" || kodi.Plot != "Keep plot" || kodi.UniqueID.Type != "imdb" || strings.TrimSpace(kodi.UniqueID.Value) != "tt1234567" || kodi.FileInfo.Width != 1920 {
		t.Fatalf("unknown fields were not preserved: %#v", kodi)
	}
	if kodi.Set.Name != "New set" || kodi.Set.Overview != "Keep overview" {
		t.Fatalf("set extension fields were not preserved: %#v", kodi.Set)
	}
	if len(kodi.Actors) != 2 || kodi.Actors[0].Name != "Alice" || kodi.Actors[0].Role != "Lead" || kodi.Actors[0].Thumb != "alice.jpg" || kodi.Actors[1].Name != "Bob" {
		t.Fatalf("actor merge did not preserve matching extensions: %#v", kodi.Actors)
	}
	if info, err := os.Stat(nfoPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("merge should preserve NFO permissions: info=%v err=%v", info, err)
	}
}

func TestExportLocalMetadataNFOEmptyManagedValuesPreserveExistingFields(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mkv")
	nfoPath := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	existing := `<?xml version="1.0"?>
<movie>
  <title>Old title</title>
  <userrating max="10">8.5</userrating>
  <tag>Third-party tag A</tag><tag>Third-party tag B</tag>
  <actor><name>Alice</name><role>Lead</role><thumb>alice.jpg</thumb></actor>
  <set><name>Old set</name><overview>Keep overview</overview></set>
</movie>`
	if err := os.WriteFile(nfoPath, []byte(existing), 0600); err != nil {
		t.Fatalf("write existing NFO: %v", err)
	}
	if _, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{
		DisplayTitle: "New title",
	}); err != nil {
		t.Fatalf("merge NFO: %v", err)
	}
	content, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatalf("read merged NFO: %v", err)
	}
	var kodi exportedMovieNFO
	if err := xml.Unmarshal(content, &kodi); err != nil {
		t.Fatalf("decode merged NFO: %v", err)
	}
	if kodi.Title != "New title" {
		t.Fatalf("title should update: %#v", kodi.Title)
	}
	if strings.TrimSpace(kodi.UserRating) != "8.5" {
		t.Fatalf("empty app rating must preserve existing userrating: %#v", kodi.UserRating)
	}
	if len(kodi.Tags) != 2 || kodi.Tags[0] != "Third-party tag A" || kodi.Tags[1] != "Third-party tag B" {
		t.Fatalf("empty app tags must preserve existing tags: %#v", kodi.Tags)
	}
	if len(kodi.Actors) != 1 || kodi.Actors[0].Name != "Alice" || kodi.Actors[0].Role != "Lead" || kodi.Actors[0].Thumb != "alice.jpg" {
		t.Fatalf("empty app people must preserve existing actors: %#v", kodi.Actors)
	}
	if kodi.Set.Name != "Old set" || kodi.Set.Overview != "Keep overview" {
		t.Fatalf("empty app collection must preserve existing set: %#v", kodi.Set)
	}
}

func TestExportLocalMetadataNFOPreservesTopLevelCommentsOnMerge(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mkv")
	nfoPath := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	existing := `<?xml version="1.0"?>
<!-- created by tinyMediaManager -->
<movie>
  <title>Old title</title>
</movie>
<!-- trailing note -->`
	if err := os.WriteFile(nfoPath, []byte(existing), 0644); err != nil {
		t.Fatalf("write existing NFO: %v", err)
	}
	if _, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{DisplayTitle: "New title"}); err != nil {
		t.Fatalf("merge NFO: %v", err)
	}
	content, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatalf("read merged NFO: %v", err)
	}
	text := string(content)
	leading := strings.Index(text, "<!-- created by tinyMediaManager -->")
	rootStart := strings.Index(text, "<movie")
	rootEnd := strings.Index(text, "</movie>")
	trailing := strings.Index(text, "<!-- trailing note -->")
	if leading < 0 || trailing < 0 {
		t.Fatalf("top-level comments were discarded:\n%s", text)
	}
	if leading > rootStart || trailing < rootEnd {
		t.Fatalf("top-level comments moved across the movie root:\n%s", text)
	}
	if _, err := parseLocalMovieNFO(content); err != nil {
		t.Fatalf("current parser cannot read merged NFO: %v", err)
	}
}

func TestExportLocalMetadataNFORejectsXMLInvalidCharactersNamingField(t *testing.T) {
	tests := []struct {
		name      string
		input     LocalMetadataNFOExportInput
		fieldName string
	}{
		{name: "control char in title", input: LocalMetadataNFOExportInput{DisplayTitle: "标题\x08损坏"}, fieldName: "标题"},
		{name: "control char in tag", input: LocalMetadataNFOExportInput{DisplayTitle: "Title", Tags: []string{"bad\x0btag"}}, fieldName: "标签"},
		{name: "control char in person", input: LocalMetadataNFOExportInput{DisplayTitle: "Title", People: []string{"bad\x01actor"}}, fieldName: "演员"},
		{name: "control char in collection", input: LocalMetadataNFOExportInput{DisplayTitle: "Title", Collection: "bad\x02set"}, fieldName: "作品集"},
		{name: "invalid utf-8 in title", input: LocalMetadataNFOExportInput{DisplayTitle: "标题\xff\xfe"}, fieldName: "标题"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			videoPath := filepath.Join(root, "movie.mp4")
			nfoPath := filepath.Join(root, "movie.nfo")
			if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
				t.Fatalf("write video: %v", err)
			}
			_, err := ExportLocalMetadataNFO(context.Background(), videoPath, test.input)
			if !errors.Is(err, ErrLocalMetadataNFOInvalid) {
				t.Fatalf("XML-invalid input error = %v", err)
			}
			if !strings.Contains(err.Error(), test.fieldName) {
				t.Fatalf("error should name field %q: %v", test.fieldName, err)
			}
			if _, statErr := os.Lstat(nfoPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed export should not create NFO: %v", statErr)
			}
		})
	}
}

func TestExportLocalMetadataNFORemovesOnlyOwnStaleTempFiles(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	staleOwn := filepath.Join(root, ".movie.nfo.tmp-1111")
	freshOwn := filepath.Join(root, ".movie.nfo.tmp-2222")
	staleOther := filepath.Join(root, ".other.nfo.tmp-3333")
	for _, path := range []string{staleOwn, freshOwn, staleOther} {
		if err := os.WriteFile(path, []byte("leftover"), 0644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
	}
	old := time.Now().Add(-2 * localMetadataTempMaxAge)
	for _, path := range []string{staleOwn, staleOther} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("age temp file: %v", err)
		}
	}
	if _, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{DisplayTitle: "Title"}); err != nil {
		t.Fatalf("export NFO: %v", err)
	}
	if _, err := os.Lstat(staleOwn); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale own temp file should be removed: %v", err)
	}
	if _, err := os.Lstat(freshOwn); err != nil {
		t.Fatalf("fresh temp file must be kept: %v", err)
	}
	if _, err := os.Lstat(staleOther); err != nil {
		t.Fatalf("other video's temp file must be kept: %v", err)
	}
}

func TestExportLocalMetadataNFORejectsInvalidExistingDocumentWithoutWriting(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	nfoPath := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	original := []byte(`<movie><title>broken</movie>`)
	if err := os.WriteFile(nfoPath, original, 0644); err != nil {
		t.Fatalf("write invalid NFO: %v", err)
	}
	_, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{DisplayTitle: "replacement"})
	if !errors.Is(err, ErrLocalMetadataNFOInvalid) {
		t.Fatalf("invalid existing NFO error = %v", err)
	}
	current, readErr := os.ReadFile(nfoPath)
	if readErr != nil || string(current) != string(original) {
		t.Fatalf("invalid existing NFO was modified: content=%q err=%v", current, readErr)
	}
}

func TestExportLocalMetadataNFOEnforcesTextDepthAndRatingBounds(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		input    LocalMetadataNFOExportInput
	}{
		{name: "oversized title", input: LocalMetadataNFOExportInput{DisplayTitle: strings.Repeat("x", localMetadataTextMaxBytes+1)}},
		{name: "invalid half rating", input: LocalMetadataNFOExportInput{DisplayTitle: "Title", PersonalRating: float64Pointer(7.2)}},
		{name: "deep existing XML", existing: `<movie>` + strings.Repeat(`<x>`, localMetadataXMLMaxDepth) + strings.Repeat(`</x>`, localMetadataXMLMaxDepth) + `</movie>`, input: LocalMetadataNFOExportInput{DisplayTitle: "Title"}},
		{name: "oversized existing XML", existing: `<movie>` + strings.Repeat(" ", int(localMetadataNFOMaxBytes)) + `</movie>`, input: LocalMetadataNFOExportInput{DisplayTitle: "Title"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			videoPath := filepath.Join(root, "movie.mp4")
			nfoPath := filepath.Join(root, "movie.nfo")
			if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
				t.Fatalf("write video: %v", err)
			}
			if test.existing != "" {
				if err := os.WriteFile(nfoPath, []byte(test.existing), 0644); err != nil {
					t.Fatalf("write existing NFO: %v", err)
				}
			}
			_, err := ExportLocalMetadataNFO(context.Background(), videoPath, test.input)
			if !errors.Is(err, ErrLocalMetadataNFOInvalid) {
				t.Fatalf("boundary error = %v", err)
			}
			if test.existing == "" {
				if _, statErr := os.Lstat(nfoPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("failed export should not create NFO: %v", statErr)
				}
			} else if content, readErr := os.ReadFile(nfoPath); readErr != nil || string(content) != test.existing {
				t.Fatalf("failed export modified existing NFO: err=%v", readErr)
			}
		})
	}
}

func TestExportLocalMetadataNFORejectsSymlinkTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	nfoPath := filepath.Join(root, "movie.nfo")
	outsidePath := filepath.Join(t.TempDir(), "outside.nfo")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(outsidePath, []byte(`<movie><title>outside</title></movie>`), 0644); err != nil {
		t.Fatalf("write outside NFO: %v", err)
	}
	if err := os.Symlink(outsidePath, nfoPath); err != nil {
		t.Fatalf("create NFO symlink: %v", err)
	}
	_, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{DisplayTitle: "replacement"})
	if !errors.Is(err, ErrLocalMetadataNFOSymlink) {
		t.Fatalf("symlink target error = %v", err)
	}
	outside, readErr := os.ReadFile(outsidePath)
	if readErr != nil || string(outside) != `<movie><title>outside</title></movie>` {
		t.Fatalf("symlink destination was modified: content=%q err=%v", outside, readErr)
	}
}

func float64Pointer(value float64) *float64 {
	return &value
}
