package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"gorm.io/gorm/logger"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestJellyfinPeopleCollectionsAndRelatedVideos(t *testing.T) {
	s, token, source, personVideo := jellyfinLibraryFixture(t)
	// Put the related fixtures outside the source folder so this test exercises relationship links.
	dir := filepath.Join(source.Directory, "relations")
	personVideo.Directory, personVideo.Path = dir, filepath.Join(dir, personVideo.Name)
	if err := database.DB.Save(&personVideo).Error; err != nil {
		t.Fatal(err)
	}
	create := func(name string) models.Video {
		v := models.Video{Name: name, Path: filepath.Join(dir, name), Directory: dir}
		if err := database.DB.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
		return v
	}
	collectionVideo, tagVideo, sameVideo, unrelated := create("collection.mp4"), create("tag.mp4"), create("same.mp4"), create("unrelated.mp4")
	hidden := create("hidden.mp4")
	if err := database.DB.Model(&hidden).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	person, hiddenPerson := models.Person{DisplayName: "讲师"}, models.Person{DisplayName: "隐藏人物"}
	for _, p := range []*models.Person{&person, &hiddenPerson} {
		if err := database.DB.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]uint{{source.ID, person.ID}, {personVideo.ID, person.ID}, {hidden.ID, person.ID}, {hidden.ID, hiddenPerson.ID}} {
		if err := database.DB.Create(&models.VideoPerson{VideoID: pair[0], PersonID: pair[1]}).Error; err != nil {
			t.Fatal(err)
		}
	}
	collection := models.MediaCollection{Name: "课程", NormalizedName: "课程"}
	if err := database.DB.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	for i, v := range []models.Video{source, collectionVideo, hidden} {
		if err := database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: v.ID, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	tag := models.Tag{Name: "内容"}
	automatic := models.Tag{Name: "短视频", AutomaticKind: "short_video"}
	for _, tag := range []*models.Tag{&tag, &automatic} {
		if err := database.DB.Create(tag).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []models.Video{source, personVideo, tagVideo, hidden} {
		if err := database.DB.Model(&v).Association("Tags").Append(&tag); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []models.Video{source, unrelated} {
		if err := database.DB.Model(&v).Association("Tags").Append(&automatic); err != nil {
			t.Fatal(err)
		}
	}
	for _, relation := range []models.VideoSameSourceRelation{
		{VideoAID: source.ID, VideoBID: sameVideo.ID, Status: models.VideoSameSourceStatusDetected},
		{VideoAID: source.ID, VideoBID: unrelated.ID, Status: models.VideoSameSourceStatusRejected},
	} {
		if err := database.DB.Create(&relation).Error; err != nil {
			t.Fatal(err)
		}
	}

	id, personID := jellyfinID(jellyVideo, source.ID), jellyfinID(jellyPerson, person.ID)
	detail := jellyfinObject(t, jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Items/"+id, token, ""))
	people := detail["People"].([]interface{})
	if len(people) != 1 || people[0].(map[string]interface{})["Id"] != personID || people[0].(map[string]interface{})["Name"] != person.DisplayName {
		t.Fatalf("people: %v", people)
	}
	personDetail := jellyfinObject(t, jellyfinRequest(s, "GET", "/Items/"+personID, token, ""))
	if personDetail["Type"] != "Person" || personDetail["Name"] != person.DisplayName {
		t.Fatal(personDetail)
	}
	for _, path := range []string{"/Items?PersonIds=" + personID, "/Items?ParentId=" + personID} {
		items, total := jellyfinItems(t, jellyfinRequest(s, "GET", path, token, ""))
		if total != 2 || len(items) != 2 {
			t.Fatalf("person videos: %v", items)
		}
	}
	peopleList, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Persons", token, ""))
	if total != 1 || peopleList[0]["Id"] != personID {
		t.Fatal(peopleList)
	}
	if w := jellyfinRequest(s, "GET", "/Items/"+jellyfinID(jellyPerson, hiddenPerson.ID), token, ""); w.Code != 404 {
		t.Fatalf("hidden person: %d", w.Code)
	}

	collectionsURL := "/Items?IncludeItemTypes=Playlist,BoxSet&ListItemIds=" + id + "&Recursive=true&SortBy=SortName"
	items, total := jellyfinItems(t, jellyfinRequest(s, "GET", collectionsURL, token, ""))
	if total != 1 || items[0]["Id"] != jellyfinID(jellyCollection, collection.ID) {
		t.Fatal(items)
	}
	if _, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?IncludeItemTypes=Playlist&ListItemIds="+id, token, "")); total != 0 {
		t.Fatal("invented playlists")
	}
	if _, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?IncludeItemTypes=BoxSet&ListItemIds="+jellyfinID(jellyVideo, unrelated.ID), token, "")); total != 0 {
		t.Fatal("lost ListItemIds filter")
	}

	for _, prefix := range []string{"/Items/", "/Movies/", "/Shows/", "/Users/" + jellyfinUserID + "/Items/"} {
		items, total = jellyfinItems(t, jellyfinRequest(s, "GET", prefix+id+"/Similar?Limit=2", token, ""))
		if total != 4 || len(items) != 2 || items[0]["Id"] != jellyfinID(jellyVideo, sameVideo.ID) {
			t.Fatalf("related videos: %d %v", total, items)
		}
	}
	items, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items/"+id+"/Similar?StartIndex=2&Limit=2", token, ""))
	if total != 4 || len(items) != 2 || items[1]["Id"] != jellyfinID(jellyVideo, personVideo.ID) {
		t.Fatal(items)
	}
	for _, path := range []string{"/Items/" + id + "/Similar?Limit=0", "/Items/" + id + "/Similar?StartIndex=4"} {
		items, total = jellyfinItems(t, jellyfinRequest(s, "GET", path, token, ""))
		if total != 4 || len(items) != 0 {
			t.Fatal(items)
		}
	}
	for _, path := range []string{"/Items?PersonIds=" + id, "/Items?ListItemIds=" + personID, "/Items/" + id + "/Similar?Unknown=true"} {
		if w := jellyfinRequest(s, "GET", path, token, ""); w.Code != 400 {
			t.Fatalf("accepted invalid query %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/Items/" + jellyfinID(jellyVideo, hidden.ID) + "/Similar", "/Items?ListItemIds=" + jellyfinID(jellyVideo, hidden.ID)} {
		if w := jellyfinRequest(s, "GET", path, token, ""); w.Code != 404 {
			t.Fatalf("hidden source %s: %d", path, w.Code)
		}
	}
	if err := database.DB.Delete(&collection).Error; err != nil {
		t.Fatal(err)
	}
	if _, total = jellyfinItems(t, jellyfinRequest(s, "GET", collectionsURL, token, "")); total != 0 {
		t.Fatal("deleted collection exposed")
	}
	if _, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items/"+id+"/Similar", token, "")); total != 3 {
		t.Fatal("deleted collection related match")
	}
	if err := database.DB.Delete(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if _, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items/"+id+"/Similar", token, "")); total != 2 {
		t.Fatal("deleted tag related match")
	}
}

func TestJellyfinPersonalRatingDisplay(t *testing.T) {
	s, token, video, _ := jellyfinLibraryFixture(t)
	path := "/Items/" + jellyfinID(jellyVideo, video.ID)
	floatPtr := func(value float64) *float64 { return &value }
	for _, rating := range []*float64{nil, floatPtr(0), floatPtr(8.5), floatPtr(10)} {
		if err := database.DB.Model(&video).Update("personal_rating", rating).Error; err != nil {
			t.Fatal(err)
		}
		item := jellyfinObject(t, jellyfinRequest(s, "GET", path, token, ""))
		if rating == nil {
			if _, ok := item["CommunityRating"]; ok {
				t.Fatal("invented rating")
			}
		} else if item["CommunityRating"] != *rating {
			t.Fatal(item["CommunityRating"])
		}
	}
}

func TestJellyfinNewEntityQueryNormalization(t *testing.T) {
	q, err := jellyfinNormalizeQuery(url.Values{"PersonIds": {"a"}, "personids": {"b"}, "ListItemIds": {"c", "d"}})
	if err != nil || q.Get("personids") != "a,b" || q.Get("listitemids") != "c,d" {
		t.Fatal(q, err)
	}
	if _, err := jellyfinTypedIDs(fmt.Sprintf("%s,%s", jellyfinID(jellyPerson, 1), jellyfinID(jellyVideo, 1)), jellyPerson); err == nil {
		t.Fatal("mixed identities accepted")
	}
}

func TestJellyfinEntityArtworkVisibilityAndPrivacy(t *testing.T) {
	s, token, video, _ := jellyfinLibraryFixture(t)
	dir := t.TempDir()
	s.people, s.collections = NewPersonService(dir), NewCollectionService(dir)
	person, err := s.people.CreatePerson("Portrait", "")
	if err != nil {
		t.Fatal(err)
	}
	collection, err := s.collections.CreateCollection("Cover", "")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.webp")
	content := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), []byte("image-content")...)
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.people.SetPersonAvatar(person.ID, source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.collections.SetCollectionCover(collection.ID, source); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.VideoPerson{PersonID: person.ID, VideoID: video.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: video.ID, Position: 1}).Error; err != nil {
		t.Fatal(err)
	}
	paths := []string{"/Items/" + jellyfinID(jellyPerson, person.ID) + "/Images/Primary", "/Items/" + jellyfinID(jellyCollection, collection.ID) + "/Images/Primary"}
	for _, path := range paths {
		for _, method := range []string{"GET", "HEAD"} {
			w := jellyfinRequest(s, method, path, token, "")
			if w.Code != 200 || w.Header().Get("Content-Type") != "image/webp" {
				t.Fatalf("%s %s: %d", method, path, w.Code)
			}
			if method == "GET" && !bytes.Equal(w.Body.Bytes(), content) {
				t.Fatal("wrong image")
			}
		}
		if w := jellyfinRequest(s, "GET", path, "", ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if err := database.DB.Model(&video).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := database.DB.Logger
	database.DB.Logger = logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Info})
	t.Cleanup(func() { database.DB.Logger = previous })
	paths = append(paths, "/Items/"+jellyfinID(jellyPerson, person.ID))
	for _, path := range paths {
		if w := jellyfinRequest(s, "GET", path, token, ""); w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	if strings.Contains(logs.String(), video.Directory) {
		t.Fatalf("scan root leaked in SQL logs")
	}
}

func TestJellyfinFilebarQueryBoundaries(t *testing.T) {
	s, token, video, _ := jellyfinLibraryFixture(t)
	for _, query := range []string{"GroupProgramsBySeries=true", "GroupProgramsBySeries=false"} {
		if w := jellyfinRequest(s, "GET", "/Items?"+query, token, ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	for _, query := range []string{"GroupProgramsBySeries=invalid", "TagIds=0", "TagIds=-1", "TagIds=" + jellyfinID(jellyPerson, 1), "IncludeItemTypes=Person&ParentId=" + jellyfinID(jellyView, 1)} {
		if w := jellyfinRequest(s, "GET", "/Items?"+query, token, ""); w.Code != 400 {
			t.Fatalf("%s: %d", query, w.Code)
		}
	}
	if _, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items/"+jellyfinID(jellyVideo, video.ID)+"/Similar?ParentId="+jellyfinID(jellyGroup, 1), token, "")); total != 0 {
		t.Fatal("similar returned folders")
	}
}

func TestJellyfinSimilarPrioritizesIndexedDirectorySiblings(t *testing.T) {
	s, token, source, sibling := jellyfinLibraryFixture(t)
	add := func(dir, name string, stale bool) models.Video {
		t.Helper()
		video := models.Video{Directory: dir, Name: name, Path: filepath.Join(dir, name), IsStale: stale}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		return video
	}
	_ = add(source.Directory, "stale.avi", true)
	subdir := filepath.Join(source.Directory, "sub")
	_ = add(subdir, "unrelated.avi", false)
	related := add(subdir, "related.avi", false)
	if err := database.DB.Create(&models.VideoSameSourceRelation{
		VideoAID: source.ID, VideoBID: related.ID, Status: models.VideoSameSourceStatusDetected,
	}).Error; err != nil {
		t.Fatal(err)
	}
	id := jellyfinID(jellyVideo, source.ID)
	base := "/Users/" + jellyfinUserID + "/Items/" + id
	for _, tc := range []struct {
		query string
		want  uint
		total int
	}{
		{"Limit=1", sibling.ID, 2},
		{"StartIndex=1&Limit=1", related.ID, 2},
		{"SearchTerm=second", sibling.ID, 1},
		{"SortBy=SortName&SortOrder=Ascending&Limit=1", related.ID, 2},
		{"ExcludeItemIds=" + jellyfinID(jellyVideo, sibling.ID), related.ID, 1},
	} {
		items, total := jellyfinItems(t, jellyfinRequest(s, "GET", base+"/Similar?"+tc.query, token, ""))
		if total != tc.total || len(items) != 1 || items[0]["Id"] != jellyfinID(jellyVideo, tc.want) {
			t.Fatalf("%s: total=%d items=%v", tc.query, total, items)
		}
	}
	if w := jellyfinRequest(s, "GET", base+"/AdditionalParts", token, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("directory siblings aren't split video parts: %d %s", w.Code, w.Body)
	}
	if w := jellyfinRequest(s, "GET", base+"/Similar", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated directory request returned %d", w.Code)
	}
}

func TestJellyfinFilebarSpecialFeaturesShowsDirectorySiblings(t *testing.T) {
	s, token, source, sibling := jellyfinLibraryFixture(t)
	add := func(directory, name, title string, stale bool) models.Video {
		t.Helper()
		video := models.Video{Directory: directory, Name: name, Path: filepath.Join(directory, name), DisplayTitle: title, IsStale: stale}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		return video
	}
	first := add(source.Directory, "z.mp4", "Alpha", false)
	tied := add(source.Directory, "a.mp4", "alpha", false)
	stale := add(source.Directory, "stale.mp4", "", true)
	deleted := add(source.Directory, "deleted.mp4", "", false)
	if err := database.DB.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	child := add(filepath.Join(source.Directory, "child"), "child.mp4", "", false)
	outside := add(t.TempDir(), "outside.mp4", "", false)
	blockedDirectory := filepath.Join(source.Directory, "blocked")
	blocked := add(blockedDirectory, "blocked.mp4", "", false)
	blockedSibling := add(source.Directory, "blocked-sibling.mp4", "", false)
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&settings).Update("scan_exclude_paths", blockedDirectory+"\n"+blockedSibling.Path).Error; err != nil {
		t.Fatal(err)
	}
	tag := models.Tag{Name: "Extras"}
	if err := database.DB.Model(&first).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.SubtitleIndexState{VideoID: first.ID, HasSidecar: true}).Error; err != nil {
		t.Fatal(err)
	}

	path := func(video models.Video) string {
		return "/Items/" + jellyfinID(jellyVideo, video.ID) + "/SpecialFeatures"
	}
	for _, client := range []string{"Filebar", "Fileball"} {
		t.Run(client, func(t *testing.T) {
			login := httptest.NewRequest("POST", "/Users/AuthenticateByName", strings.NewReader(`{"Username":"viewer","Pw":"test-password"}`))
			login.RemoteAddr = "127.0.0.1:2"
			login.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client=%q`, client))
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, login)
			sessionToken, _ := jellyfinObject(t, w)["AccessToken"].(string)
			if sessionToken == "" {
				t.Fatal("missing session token")
			}
			for _, prefix := range []string{"/Users/" + jellyfinUserID, ""} {
				w := jellyfinRequest(s, "GET", prefix+path(source), sessionToken, "")
				var items []struct {
					Id, Name     string
					Tags         []string
					HasSubtitles bool
				}
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != 3 {
					t.Fatalf("expected three visible siblings: %d %s", w.Code, w.Body)
				}
				for i, want := range []models.Video{first, tied, sibling} {
					if items[i].Id != jellyfinID(jellyVideo, want.ID) {
						t.Fatalf("wrong sibling order: %+v", items)
					}
				}
				if items[0].Name != "Alpha" || len(items[0].Tags) != 1 || items[0].Tags[0] != "Extras" || !items[0].HasSubtitles {
					t.Fatalf("incomplete sibling DTO: %+v", items[0])
				}
			}
			if w := jellyfinRequest(s, "GET", path(child), sessionToken, ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
				t.Fatalf("expected an empty array for a single video directory: %d %s", w.Code, w.Body)
			}
			for _, hidden := range []models.Video{stale, deleted, outside, blocked, blockedSibling} {
				if w := jellyfinRequest(s, "GET", path(hidden), sessionToken, ""); w.Code != http.StatusNotFound {
					t.Fatalf("invisible source returned %d", w.Code)
				}
			}
		})
	}
	if w := jellyfinRequest(s, "GET", path(source), token, ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("non-Filebar client should receive an empty array: %d %s", w.Code, w.Body)
	}
	if w := jellyfinRequest(s, "GET", path(source), "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated SpecialFeatures returned %d", w.Code)
	}
}
