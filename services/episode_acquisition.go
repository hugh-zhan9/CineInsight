package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

var ErrEpisodeSeriesConfirmation = errors.New("episode_series_confirmation_required: 请先核对不同系列名称")

var ErrEpisodeContextChanged = errors.New("episode_context_changed: 季集资料或来源已变化，请重新核对")

type EpisodeResourceSearchRequest struct {
	CollectionID    uint  `json:"collection_id"`
	Season          int   `json:"season"`
	Episodes        []int `json:"episodes"`
	SeriesConfirmed bool  `json:"series_confirmed"`
}
type EpisodeResourceSearch struct {
	OriginToken     string    `json:"origin_token"`
	ExpiresAt       time.Time `json:"expires_at" ts_type:"string"`
	CollectionID    uint      `json:"collection_id"`
	CollectionTitle string    `json:"collection_title"`
	Season          int       `json:"season"`
	Targets         []int     `json:"targets"`
	Query           string    `json:"query"`
	Notice          string    `json:"notice"`
	ReportSignature string    `json:"report_signature"`
}
type EpisodeFileChoice struct {
	FileID  string `json:"file_id"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
}

func requireEpisodeFields(data []byte, names ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range names {
		value, ok := fields[name]
		if !ok || strings.TrimSpace(string(value)) == "null" {
			return fmt.Errorf("invalid_input: 必须明确填写%s", name)
		}
	}
	return nil
}
func (r *EpisodeResourceSearchRequest) UnmarshalJSON(data []byte) error {
	if err := requireEpisodeFields(data, "season"); err != nil {
		return err
	}
	type plain EpisodeResourceSearchRequest
	return json.Unmarshal(data, (*plain)(r))
}
func (r *EpisodeFileChoice) UnmarshalJSON(data []byte) error {
	if err := requireEpisodeFields(data, "season", "episode"); err != nil {
		return err
	}
	type plain EpisodeFileChoice
	return json.Unmarshal(data, (*plain)(r))
}

type EpisodeDownloadRequest struct {
	DraftID            string              `json:"draft_id"`
	ExpectedRevision   uint64              `json:"expected_revision"`
	SelectedFileIDs    []string            `json:"selected_file_ids"`
	OriginToken        string              `json:"origin_token"`
	FileEpisodeChoices []EpisodeFileChoice `json:"file_episode_choices"`
}
type episodeSearchSnapshot struct {
	view      EpisodeResourceSearch
	revision  int64
	db        *gorm.DB
	owner     context.Context
	taskID    string
	selection string
}
type episodeAssociationSnapshot struct {
	owner           context.Context
	db              *gorm.DB
	expires         time.Time
	collectionID    uint
	rootRevision    int64
	reportSignature string
	items           []episodeAssociationEntry
	result          *EpisodeAssociationResult
}

func normalizeEpisodeSearch(request EpisodeResourceSearchRequest) (EpisodeResourceSearchRequest, error) {
	if request.CollectionID == 0 || request.CollectionID > experienceMaxSafeInteger || request.Season < 0 || request.Season > 999 || len(request.Episodes) == 0 || len(request.Episodes) > 200 {
		return request, errors.New("invalid_input: 请选择作品集、季和1至200个缺集")
	}
	seen := map[int]bool{}
	for _, episode := range request.Episodes {
		if episode < 1 || episode > 1000000 || seen[episode] {
			return request, errors.New("invalid_input: 集号必须有效且互不重复")
		}
		seen[episode] = true
	}
	request.Episodes = append([]int{}, request.Episodes...)
	sort.Ints(request.Episodes)
	return request, nil
}
func episodeTargetsMissing(report *CollectionEpisodeReport, season int, episodes []int) bool {
	for _, summary := range report.Seasons {
		if summary.Season != season {
			continue
		}
		for _, episode := range episodes {
			missing := false
			for _, gap := range summary.MissingRanges {
				if episode >= gap.Start && episode <= gap.End {
					missing = true
					break
				}
			}
			if !missing {
				return false
			}
		}
		return true
	}
	return false
}
func (s *CollectionService) pruneEpisodePreviewsLocked() {
	for token, p := range s.episodeSearches {
		if p.db != database.DB || p.owner.Err() != nil || time.Now().After(p.view.ExpiresAt) {
			delete(s.episodeSearches, token)
		}
	}
	for token, p := range s.episodeAssociations {
		if p.db != database.DB || p.owner.Err() != nil || time.Now().After(p.expires) {
			delete(s.episodeAssociations, token)
		}
	}
}

// PrepareEpisodeResourceSearch is local/read-only; resource searches still need a separate user action.
func (s *CollectionService) PrepareEpisodeResourceSearch(ctx context.Context, request EpisodeResourceSearchRequest) (*EpisodeResourceSearch, error) {
	owner := ctx
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := normalizeEpisodeSearch(request)
	if err != nil {
		return nil, err
	}
	report, err := s.GetEpisodeReportContext(ctx, request.CollectionID, 0, 200)
	if err != nil {
		return nil, err
	}
	if report.MixedSeries && !request.SeriesConfirmed {
		return nil, ErrEpisodeSeriesConfirmation
	}
	if !episodeTargetsMissing(report, request.Season, request.Episodes) {
		return nil, ErrEpisodeContextChanged
	}
	name := strings.TrimSpace(report.sourceName)
	if name == "" {
		return nil, errors.New("invalid_input: 请先填写作品集名称")
	}
	queryName := name
	notice := ""
	if utf8.RuneCountInString(queryName) > 160 {
		queryName = string([]rune(queryName)[:160])
		notice = "搜索关键词已按长度限制缩短，可在搜索框修改。"
	}
	query := fmt.Sprintf("%s S%02d", queryName, request.Season)
	if len(request.Episodes) == 1 {
		query += fmt.Sprintf("E%02d", request.Episodes[0])
	}
	view := EpisodeResourceSearch{OriginToken: uuid.NewString(), ExpiresAt: time.Now().Add(10 * time.Minute), CollectionID: request.CollectionID, CollectionTitle: name, Season: request.Season, Targets: request.Episodes, Query: query, Notice: notice, ReportSignature: report.sourceSignature}
	s.episodeMu.Lock()
	defer s.episodeMu.Unlock()
	s.pruneEpisodePreviewsLocked()
	if len(s.episodeSearches) >= 16 {
		return nil, errors.New("invalid_input: 待确认搜索过多，请稍后重试")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.episodeSearches == nil {
		s.episodeSearches = map[string]*episodeSearchSnapshot{}
	}
	s.episodeSearches[view.OriginToken] = &episodeSearchSnapshot{view: view, revision: report.sourceRevision, db: database.DB, owner: owner}
	s.episodeSearches[view.OriginToken].view.Targets = append([]int{}, view.Targets...)
	return &view, nil
}

// storeEpisodeOrigin runs inside the BT selection transaction, before its wake-up.
func storeEpisodeOrigin(ctx context.Context, tx *gorm.DB, snapshot *episodeSearchSnapshot, request EpisodeDownloadRequest, task models.BTTask, files []models.BTFile) error {
	report, err := collectionEpisodeReportFrom(ctx, tx, snapshot.view.CollectionID, 0, 200)
	if err != nil {
		return err
	}
	if report.sourceSignature != snapshot.view.ReportSignature || !episodeTargetsMissing(report, snapshot.view.Season, snapshot.view.Targets) {
		return ErrEpisodeContextChanged
	}
	if err := claimCollectionMutation(tx, snapshot.view.CollectionID, snapshot.revision); err != nil {
		if errors.Is(err, ErrCollectionOrderConflict) {
			return ErrEpisodeContextChanged
		}
		return err
	}
	wanted := map[string]bool{}
	for _, id := range request.SelectedFileIDs {
		wanted[id] = true
	}
	choices := map[string]EpisodeFileChoice{}
	for _, choice := range request.FileEpisodeChoices {
		if !wanted[choice.FileID] || choice.Season < 0 || choice.Season > 999 || choice.Episode < 1 || choice.Episode > 1000000 {
			return errors.New("invalid_input: 文件季集映射无效")
		}
		if _, exists := choices[choice.FileID]; exists {
			return errors.New("invalid_input: 文件映射不能重复")
		}
		choices[choice.FileID] = choice
	}
	requested, _ := json.Marshal(snapshot.view.Targets)
	videos := 0
	for _, file := range files {
		if !wanted[file.ManifestFileID] {
			continue
		}
		if btFileKind(file.RelativePath) != "video" {
			if _, ok := choices[file.ManifestFileID]; ok {
				return errors.New("invalid_input: 只能为视频填写季集")
			}
			continue
		}
		videos++
		row := models.EpisodeAcquisitionOrigin{ID: uuid.NewString(), TaskID: task.ID, FileID: file.ManifestFileID, CollectionID: snapshot.view.CollectionID, CollectionTitle: snapshot.view.CollectionTitle, RequestedSeason: snapshot.view.Season, RequestedEpisodesJSON: string(requested), Revision: 1}
		if choice, ok := choices[file.ManifestFileID]; ok {
			if btFileKind(file.RelativePath) != "video" {
				return errors.New("invalid_input: 只能为视频填写季集")
			}
			row.MappedSeason = &choice.Season
			row.MappedEpisode = &choice.Episode
			row.ConfirmedMapping = true
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	if videos == 0 {
		return errors.New("invalid_input: 补集下载至少选择一个视频")
	}
	return nil
}

// SubmitEpisodeDownload reuses the original BT admission, cache and task protocol.
func (s *BTService) SubmitEpisodeDownload(ctx context.Context, collections *CollectionService, request EpisodeDownloadRequest) (BTTaskView, error) {
	if collections == nil {
		return BTTaskView{}, ErrEpisodeContextChanged
	}
	if request.ExpectedRevision == 0 || request.ExpectedRevision >= experienceMaxSafeInteger || len(request.FileEpisodeChoices) > 200 {
		return BTTaskView{}, errors.New("invalid_input: 下载选择无效")
	}
	request.SelectedFileIDs = append([]string{}, request.SelectedFileIDs...)
	sort.Strings(request.SelectedFileIDs)
	request.FileEpisodeChoices = append([]EpisodeFileChoice{}, request.FileEpisodeChoices...)
	sort.Slice(request.FileEpisodeChoices, func(i, j int) bool {
		return request.FileEpisodeChoices[i].FileID < request.FileEpisodeChoices[j].FileID
	})
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return BTTaskView{}, err
	}
	defer unlock()
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return BTTaskView{}, err
	}
	defer leave()
	if err = s.loadClients(ctx); err != nil {
		return BTTaskView{}, err
	}
	if err = s.writeAllowed(ctx); err != nil {
		return BTTaskView{}, err
	}
	collections.episodeMu.Lock()
	defer collections.episodeMu.Unlock()
	collections.pruneEpisodePreviewsLocked()
	preview := collections.episodeSearches[request.OriginToken]
	if preview == nil {
		return BTTaskView{}, ErrEpisodeContextChanged
	}
	selection, err := json.Marshal(struct {
		Files   []string
		Choices []EpisodeFileChoice
	}{request.SelectedFileIDs, request.FileEpisodeChoices})
	if err != nil {
		return BTTaskView{}, err
	}
	digest := btDigest(selection)
	if preview.taskID != "" {
		if preview.taskID != request.DraftID || preview.selection != digest {
			return BTTaskView{}, ErrEpisodeContextChanged
		}
		return s.Get(ctx, preview.taskID)
	}
	var view BTTaskView
	err = s.withTaskCache(ctx, request.DraftID, func(scoped context.Context, _ *gorm.DB, _ *btCacheFiles) error {
		var err error
		view, err = s.submitTaskWithOriginLocked(scoped, BTSubmitRequest{DraftID: request.DraftID, ExpectedRevision: request.ExpectedRevision, SelectedFileIDs: request.SelectedFileIDs}, func(tx *gorm.DB, task models.BTTask, files []models.BTFile) error {
			return storeEpisodeOrigin(scoped, tx, preview, request, task, files)
		}, func() { preview.taskID = request.DraftID; preview.selection = digest })
		return err
	})
	return view, err
}

func requestedEpisodeContains(encoded string, episode int) bool {
	var episodes []int
	if json.Unmarshal([]byte(encoded), &episodes) != nil {
		return false
	}
	for _, item := range episodes {
		if item == episode {
			return true
		}
	}
	return false
}
