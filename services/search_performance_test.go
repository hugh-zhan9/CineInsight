package services

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

type searchPerfCase struct {
	Name, Word, Shape string
	Compound          bool
}
type searchPerfStat struct {
	Rows                               int `json:"rows"`
	Variant, Group, Shape, Temperature string
	SamplesMS                          []float64 `json:"samples_ms"`
	P50MS, P95MS                       float64
	AllocBytesP50                      uint64
	Matches                            int64
	ResultSHA256                       string
	Plan                               []string
}
type searchPerfStorage struct {
	Rows                        int
	Variant                     string
	DataBytes, IndexBytes       int64
	IndexBuildMS                float64
	WriteP50MS, DeleteP50MS     float64
	GoHeapBytes, GoHeapReserved uint64
	ProcessRSSBytes             *uint64
}
type searchPerfReport struct {
	Backend, GoVersion, Platform, CPU, ColdDefinition, Workload string
	Samples                                                     int
	CompileOptions                                              []string
	FTS5                                                        bool
	FTS5Note                                                    string
	ServerVersion                                               string
	Statistics                                                  []searchPerfStat
	Storage                                                     []searchPerfStorage
	Integrity                                                   []string
}

var searchPerfCases = []searchPerfCase{
	{"common", "人生", "groups", false}, {"rare", "独有暗号", "flat", false}, {"missing", "不存在词语", "flat", false},
	{"chinese_1", "海", "flat", false}, {"chinese_2", "海边", "flat", false}, {"chinese_3", "海边日", "flat", false},
	{"case", "MiXeD", "flat", false}, {"punctuation", "，你好！", "flat", false}, {"percent", "%", "flat", false}, {"underscore", "_", "flat", false}, {"backslash", `\`, "flat", false},
	{"combined", "海边", "groups", true}, {"within_video", "人生", "video_hits", false},
}

func searchPerfText(i int) string {
	text := fmt.Sprintf("人生对白 %d", i%97)
	if i%10 == 0 {
		text += " 海边日落"
	}
	if i%7 == 0 {
		text += " MiXeD"
	}
	if i%11 == 0 {
		text += " ，你好！"
	}
	if i%13 == 0 {
		text += ` 100%_path\value`
	}
	if i%100003 == 0 {
		text += " 独有暗号"
	}
	return text
}
func searchPerfVisible(video int) bool { return video%29 != 0 && video%37 != 0 }
func searchPerfWriteHash(h hash.Hash, id, video uint64, start, end int64) {
	var bytes [32]byte
	binary.LittleEndian.PutUint64(bytes[0:8], id)
	binary.LittleEndian.PutUint64(bytes[8:16], video)
	binary.LittleEndian.PutUint64(bytes[16:24], uint64(start))
	binary.LittleEndian.PutUint64(bytes[24:32], uint64(end))
	h.Write(bytes[:])
}
func searchPerfRequest(item searchPerfCase) DialogueSearchRequest {
	filter := LibraryFilter{}
	if item.Compound {
		filter = LibraryFilter{PersonIDs: []uint{1}, TagIDs: []uint{1}, MaxDurationSeconds: 4200, FavoritesOnly: true, WatchState: RandomWatchUnwatched}
	}
	request, _ := normalizeDialogueRequest(DialogueSearchRequest{Query: item.Word, Filter: &filter, Limit: 30, VideoID: 1}, false)
	if item.Shape != "video_hits" {
		request.VideoID = 0
	}
	return request
}
func searchPerfQuery(db *gorm.DB, item searchPerfCase, full bool) (*gorm.DB, error) {
	query, err := dialogueMatches(db, searchPerfRequest(item))
	if err != nil {
		return nil, err
	}
	if full {
		return query.Select("subtitle_segments.id,video_id,start_time_ms,end_time_ms").Order("subtitle_segments.id ASC"), nil
	}
	if item.Shape == "groups" {
		return query.Select("video_id,COUNT(*) AS hit_count").Group("video_id").Order("video_id ASC").Limit(21), nil
	}
	if item.Shape == "video_hits" {
		return query.Select("id,video_id,start_time_ms,end_time_ms").Order("start_time_ms ASC,id ASC").Limit(31), nil
	}
	return query.Select("id,video_id,start_time_ms,end_time_ms").Order("video_id DESC,start_time_ms ASC,segment_index ASC").Limit(51), nil
}
func searchPerfExpected(rows int, item searchPerfCase) (string, int64) {
	sum := sha256.New()
	var count int64
	for i := 0; i < rows; i++ {
		video := i/1000 + 1
		if !searchPerfVisible(video) || item.Shape == "video_hits" && video != 1 {
			continue
		}
		if item.Compound && (video%2 != 0 || video%3 != 0 || video%7 == 0 || 1800+(video%5)*1200 > 4200) {
			continue
		}
		if !strings.Contains(strings.ToLower(searchPerfText(i)), strings.ToLower(item.Word)) {
			continue
		}
		start := int64(i%1000) * 1000
		searchPerfWriteHash(sum, uint64(i+1), uint64(video), start, start+800)
		count++
	}
	return fmt.Sprintf("%x", sum.Sum(nil)), count
}
func searchPerfFingerprint(t *testing.T, db *gorm.DB, item searchPerfCase) (string, int64) {
	t.Helper()
	query, err := searchPerfQuery(db, item, true)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := query.Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	sum := sha256.New()
	var count int64
	for rows.Next() {
		var id, video uint64
		var start, end int64
		if err := rows.Scan(&id, &video, &start, &end); err != nil {
			t.Fatal(err)
		}
		searchPerfWriteHash(sum, id, video, start, end)
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sum.Sum(nil)), count
}
func searchPerfPlan(t *testing.T, db *gorm.DB, item searchPerfCase) []string {
	t.Helper()
	query, err := searchPerfQuery(db, item, false)
	if err != nil {
		t.Fatal(err)
	}
	dry := query.Session(&gorm.Session{DryRun: true}).Find(&[]map[string]any{})
	prefix := "EXPLAIN QUERY PLAN "
	if dbtest.IsPostgres() {
		prefix = "EXPLAIN "
	}
	rows, err := db.Raw(prefix+dry.Statement.SQL.String(), dry.Statement.Vars...).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		if dbtest.IsPostgres() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			result = append(result, line)
		} else {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			result = append(result, detail)
		}
	}
	return result
}
func searchPerfPercentile(values []float64, p float64) float64 {
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)
	return sorted[min(len(sorted)-1, int(float64(len(sorted)-1)*p))]
}
func searchPerfRun(t *testing.T, db *gorm.DB, item searchPerfCase, cold bool) (float64, uint64) {
	t.Helper()
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	active := db
	var closeConnection func()
	if cold {
		pool.SetMaxIdleConns(0)
		connection, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		active = db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
		active.Statement.ConnPool = connection
		closeConnection = func() { connection.Close(); pool.SetMaxIdleConns(1) }
	}
	previous := database.DB
	database.DB = active
	query, err := searchPerfQuery(active, item, false)
	if err == nil {
		var rows []map[string]any
		err = query.Find(&rows).Error
	}
	database.DB = previous
	if closeConnection != nil {
		closeConnection()
	}
	elapsed := float64(time.Since(start)) / float64(time.Millisecond)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	return elapsed, after.TotalAlloc - before.TotalAlloc
}
func searchPerfStorageSize(t *testing.T, db *gorm.DB) (int64, int64) {
	t.Helper()
	if dbtest.IsPostgres() {
		var data, index int64
		if err := db.Raw("SELECT pg_table_size('subtitle_segments'),pg_indexes_size('subtitle_segments')").Row().Scan(&data, &index); err != nil {
			t.Fatal(err)
		}
		return data, index
	}
	var pages, pageSize int64
	db.Raw("PRAGMA page_count").Scan(&pages)
	db.Raw("PRAGMA page_size").Scan(&pageSize)
	var index int64
	// dbstat is optional; its absence is reported as -1 instead of invented bytes.
	if err := db.Raw("SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name IN (SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='subtitle_segments')").Scan(&index).Error; err != nil {
		index = -1
	}
	return pages * pageSize, index
}
func searchPerfRSS() *uint64 {
	raw, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return nil
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return nil
	}
	value *= 1024
	return &value
}
func searchPerfWrites(t *testing.T, db *gorm.DB) (float64, float64) {
	t.Helper()
	writes, deletes := []float64{}, []float64{}
	for round := 0; round < 30; round++ {
		batch := make([]models.SubtitleSegment, 100)
		for i := range batch {
			batch[i] = models.SubtitleSegment{VideoID: 1, SegmentIndex: 10000 + round*100 + i, StartTimeMs: int64(i), EndTimeMs: int64(i + 1), Text: "__perf_write__", SubtitlePath: "/synthetic/write.srt"}
		}
		start := time.Now()
		if err := db.CreateInBatches(&batch, 100).Error; err != nil {
			t.Fatal(err)
		}
		writes = append(writes, float64(time.Since(start))/float64(time.Millisecond))
		ids := make([]uint, len(batch))
		for i := range batch {
			ids[i] = batch[i].ID
		}
		start = time.Now()
		if err := db.Where("id IN ?", ids).Delete(&models.SubtitleSegment{}).Error; err != nil {
			t.Fatal(err)
		}
		deletes = append(deletes, float64(time.Since(start))/float64(time.Millisecond))
	}
	return searchPerfPercentile(writes, .5), searchPerfPercentile(deletes, .5)
}
func searchPerfSeed(t *testing.T, db *gorm.DB, count int) {
	t.Helper()
	if err := db.AutoMigrate(&models.Video{}, &models.Settings{}, &models.ScanDirectory{}, &models.Person{}, &models.VideoPerson{}, &models.SubtitleSegment{}, &models.SubtitleIndexState{}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{&models.Settings{VideoExtensions: ".mp4"}, &models.Person{ID: 1, DisplayName: "合成人物"}, &models.Tag{ID: 1, Name: "合成标签"}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for id := 1; id <= max(1, (count+999)/1000); id++ {
		video := models.Video{ID: uint(id), Name: fmt.Sprintf("synthetic-%d.mp4", id), Path: fmt.Sprintf("/synthetic/%d.mp4", id), Directory: "/synthetic", Duration: float64(1800 + (id%5)*1200), IsFavorite: id%2 == 0, IsWatched: id%7 == 0, IsStale: id%29 == 0}
		if err := db.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		if id%2 == 0 {
			if err := db.Exec("INSERT INTO video_tags (video_id,tag_id) VALUES (?,1)", id).Error; err != nil {
				t.Fatal(err)
			}
		}
		if id%3 == 0 {
			if err := db.Create(&models.VideoPerson{VideoID: uint(id), PersonID: 1}).Error; err != nil {
				t.Fatal(err)
			}
		}
		if id%37 == 0 {
			if err := db.Delete(&video).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for offset := 0; offset < count; offset += 1000 {
		batch := make([]models.SubtitleSegment, 0, min(1000, count-offset))
		for i := offset; i < min(offset+1000, count); i++ {
			start := int64(i%1000) * 1000
			batch = append(batch, models.SubtitleSegment{VideoID: uint(i/1000 + 1), SegmentIndex: i % 1000, StartTimeMs: start, EndTimeMs: start + 800, Text: searchPerfText(i), SubtitlePath: "/synthetic/source.srt"})
		}
		if err := db.CreateInBatches(&batch, 1000).Error; err != nil {
			t.Fatal(err)
		}
	}
}
func searchPerfIntegrity(t *testing.T, db *gorm.DB, count int) {
	t.Helper()
	if count == 0 {
		return
	}
	var row models.SubtitleSegment
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	query := searchPerfCase{Word: "人生", Shape: "flat"}
	before, n := searchPerfFingerprint(t, db, query)
	if err := db.Model(&row).Update("text", "temporary-change").Error; err != nil {
		t.Fatal(err)
	}
	_, after := searchPerfFingerprint(t, db, query)
	if after != n-1 {
		t.Fatal("source update left a ghost hit")
	}
	if err := db.Delete(&row).Error; err != nil {
		t.Fatal(err)
	}
	row.Text = searchPerfText(0)
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	restored, _ := searchPerfFingerprint(t, db, query)
	if restored != before {
		t.Fatal("delete/restore changed positions")
	}
	if err := db.Delete(&models.Video{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	_, hidden := searchPerfFingerprint(t, db, searchPerfCase{Word: "人生", Shape: "video_hits"})
	if hidden != 0 {
		t.Fatal("deleted video remained searchable")
	}
	if err := db.Unscoped().Model(&models.Video{}).Where("id = 1").UpdateColumn("deleted_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	restored, _ = searchPerfFingerprint(t, db, query)
	if restored != before {
		t.Fatal("restored video positions changed")
	}
}
func TestLibrarySearchPerformanceEvaluation(t *testing.T) {
	if os.Getenv("CINEINSIGHT_SEARCH_PERF") != "1" {
		t.Skip("explicit synthetic performance evaluation only")
	}
	output := os.Getenv("CINEINSIGHT_SEARCH_PERF_OUT")
	if output == "" {
		t.Fatal("CINEINSIGHT_SEARCH_PERF_OUT is required")
	}
	report := searchPerfReport{Backend: dbtest.Backend(), GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, CPU: fmt.Sprintf("GOMAXPROCS=%d; package/test concurrency controlled by agent-go", runtime.GOMAXPROCS(0)), Samples: 30, ColdDefinition: "new dedicated database connection, including connection setup; OS and PostgreSQL shared buffers are NOT purged", Workload: "synthetic only; 1000 cues/video, common/rare/missing, Chinese 1/2/3 characters, ASCII case, punctuation and escaped wildcard literals; stale/deleted videos excluded; compound AND person/tag/watch/favorite/duration", Statistics: []searchPerfStat{}, Storage: []searchPerfStorage{}, Integrity: []string{}}
	sizes := []int{0, 100, 10000, 100000, 1000000}
	if value := os.Getenv("CINEINSIGHT_SEARCH_PERF_ROWS"); value != "" {
		sizes = nil
		for _, part := range strings.Split(value, ",") {
			number, err := strconv.Atoi(part)
			if err != nil || number < 0 || number > 1000000 {
				t.Fatal("invalid size")
			}
			sizes = append(sizes, number)
		}
	}
	save := func() {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range sizes {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			db := dbtest.OpenRaw(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
			pool, _ := db.DB()
			pool.SetMaxOpenConns(1)
			pool.SetMaxIdleConns(1)
			previous := database.DB
			database.DB = db
			defer func() { database.DB = previous }()
			searchPerfSeed(t, db, size)
			if dbtest.IsPostgres() {
				if err := db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec("CREATE INDEX idx_subtitle_segments_text_trgm ON subtitle_segments USING GIN (LOWER(text) gin_trgm_ops)").Error; err != nil {
					t.Fatal(err)
				}
				db.Raw("SHOW server_version").Scan(&report.ServerVersion)
			} else if report.CompileOptions == nil {
				db.Raw("PRAGMA compile_options").Scan(&report.CompileOptions)
				err := db.Exec("CREATE VIRTUAL TABLE perf_fts_probe USING fts5(text,tokenize='trigram')").Error
				report.FTS5 = err == nil
				if err != nil {
					report.FTS5Note = "packaged go-sqlite3 build cannot create FTS5 trigram table"
				} else {
					db.Exec("INSERT INTO perf_fts_probe(text) VALUES ('海边日落')")
					var one, two int64
					db.Raw("SELECT COUNT(*) FROM perf_fts_probe WHERE perf_fts_probe MATCH '海'").Scan(&one)
					db.Raw("SELECT COUNT(*) FROM perf_fts_probe WHERE perf_fts_probe MATCH '海边'").Scan(&two)
					report.FTS5Note = fmt.Sprintf("MATCH short-word negative control: 1-char=%d,2-char=%d; not a semantics-preserving replacement", one, two)
				}
			}
			expected := map[string]string{}
			counts := map[string]int64{}
			for _, item := range searchPerfCases {
				expected[item.Name], counts[item.Name] = searchPerfExpected(size, item)
			}
			for _, variant := range []string{"current", "ordered_candidate"} {
				buildMS := 0.0
				if variant == "ordered_candidate" {
					start := time.Now()
					if err := db.Exec("CREATE INDEX idx_perf_video_time ON subtitle_segments (video_id DESC,start_time_ms ASC,id ASC)").Error; err != nil {
						t.Fatal(err)
					}
					buildMS = float64(time.Since(start)) / float64(time.Millisecond)
				}
				if err := db.Exec("ANALYZE").Error; err != nil {
					t.Fatal(err)
				}
				for _, item := range searchPerfCases {
					digest, matches := searchPerfFingerprint(t, db, item)
					if digest != expected[item.Name] || matches != counts[item.Name] {
						t.Fatalf("substring/position mismatch %s %s", variant, item.Name)
					}
					plan := searchPerfPlan(t, db, item)
					for _, temperature := range []string{"warm", "connection_cold"} {
						samples := []float64{}
						allocations := []uint64{}
						if temperature == "warm" {
							searchPerfRun(t, db, item, false)
						}
						for i := 0; i < report.Samples; i++ {
							elapsed, allocated := searchPerfRun(t, db, item, temperature == "connection_cold")
							samples = append(samples, elapsed)
							allocations = append(allocations, allocated)
						}
						sort.Slice(allocations, func(i, j int) bool { return allocations[i] < allocations[j] })
						report.Statistics = append(report.Statistics, searchPerfStat{Rows: size, Variant: variant, Group: item.Name, Shape: item.Shape, Temperature: temperature, SamplesMS: samples, P50MS: searchPerfPercentile(samples, .5), P95MS: searchPerfPercentile(samples, .95), AllocBytesP50: allocations[len(allocations)/2], Matches: matches, ResultSHA256: digest, Plan: plan})
						save()
					}
				}
				searchPerfIntegrity(t, db, size)
				if size == 0 {
					report.Integrity = append(report.Integrity, fmt.Sprintf("%d/%s: empty result equivalence passed; no rows to mutate", size, variant))
				} else {
					report.Integrity = append(report.Integrity, fmt.Sprintf("%d/%s: full result hashes, source edits, delete/restore passed", size, variant))
				}
				writeMS, deleteMS := searchPerfWrites(t, db)
				dataBytes, indexBytes := searchPerfStorageSize(t, db)
				var memory runtime.MemStats
				runtime.ReadMemStats(&memory)
				report.Storage = append(report.Storage, searchPerfStorage{Rows: size, Variant: variant, DataBytes: dataBytes, IndexBytes: indexBytes, IndexBuildMS: buildMS, WriteP50MS: writeMS, DeleteP50MS: deleteMS, GoHeapBytes: memory.HeapAlloc, GoHeapReserved: memory.HeapSys, ProcessRSSBytes: searchPerfRSS()})
				save()
			}
		})
	}
	save()
}
