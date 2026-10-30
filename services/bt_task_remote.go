package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"video-master/models"
	"video-master/services/btclient"
)

var errBTMissing = errors.New("download_missing: 客户端未发现任务")

func btCandidateHashes(t models.BTTask) []string {
	out := []string{}
	add := func(h string) {
		if h == "" {
			return
		}
		for _, x := range out {
			if x == h {
				return
			}
		}
		out = append(out, h)
	}
	add(t.QBHash)
	if t.InfoHashV1 != nil {
		add(*t.InfoHashV1)
	}
	if t.InfoHashV2 != nil && len(*t.InfoHashV2) == 64 {
		add((*t.InfoHashV2)[:40])
	}
	return out
}
func btHashMatches(t models.BTTask, p btclient.QBProperties) bool {
	return (t.InfoHashV1 == nil || *t.InfoHashV1 == p.InfoHashV1) && (t.InfoHashV2 == nil || *t.InfoHashV2 == p.InfoHashV2) && (t.InfoHashV1 != nil || t.InfoHashV2 != nil)
}
func btOwner(t models.BTTask, remote btclient.QBTorrent) bool {
	tag := false
	for _, x := range strings.Split(remote.Tags, ",") {
		if strings.TrimSpace(x) == t.OwnerTag {
			tag = true
		}
	}
	return tag && remote.Category == "CineInsight" && !remote.AutoTMM && filepath.Clean(remote.SavePath) == filepath.Clean(t.CanonicalRoot)
}
func (s *BTService) findRemote(ctx context.Context, t models.BTTask) (btclient.QBTorrent, btclient.QBProperties, error) {
	s.mu.Lock()
	client := s.remote
	s.mu.Unlock()
	return btFindRemote(ctx, client, t)
}
func btFindRemote(ctx context.Context, client BTRemote, t models.BTTask) (btclient.QBTorrent, btclient.QBProperties, error) {
	if client == nil {
		return btclient.QBTorrent{}, btclient.QBProperties{}, ErrBTMaintenance
	}
	var found *btclient.QBTorrent
	for _, hash := range btCandidateHashes(t) {
		rows, err := client.QBTorrents(ctx, hash)
		if err != nil {
			return btclient.QBTorrent{}, btclient.QBProperties{}, err
		}
		for _, row := range rows {
			if found != nil && found.Hash != row.Hash {
				return row, btclient.QBProperties{}, ErrBTChanged
			}
			copy := row
			found = &copy
		}
	}
	if found == nil {
		return btclient.QBTorrent{}, btclient.QBProperties{}, errBTMissing
	}
	if !btOwner(t, *found) {
		return *found, btclient.QBProperties{}, ErrBTExternal
	}
	properties, err := client.QBProperties(ctx, found.Hash)
	if err != nil {
		return *found, properties, err
	}
	if !btHashMatches(t, properties) || (properties.Hash != "" && properties.Hash != found.Hash) {
		return *found, properties, ErrBTChanged
	}
	return *found, properties, nil
}
func btMapFiles(files []models.BTFile, remote []btclient.QBFile) (map[string]btclient.QBFile, error) {
	byPath := map[string]btclient.QBFile{}
	for _, row := range remote {
		if strings.Contains(row.Name, "\\") || filepath.IsAbs(row.Name) || row.Name == "" || filepath.ToSlash(filepath.Clean(row.Name)) != row.Name {
			return nil, ErrBTChanged
		}
		if _, exists := byPath[row.Name]; exists {
			return nil, ErrBTChanged
		}
		byPath[row.Name] = row
	}
	result := map[string]btclient.QBFile{}
	for _, file := range files {
		if file.Padding {
			continue
		}
		row, ok := byPath[file.RelativePath]
		if !ok || row.Size != file.Size {
			return nil, ErrBTChanged
		}
		result[file.ManifestFileID] = row
		delete(byPath, file.RelativePath)
	}
	if len(byPath) != 0 {
		return nil, ErrBTChanged
	}
	return result, nil
}
func btSelectionMatches(files []models.BTFile, mapping map[string]btclient.QBFile) bool {
	for _, file := range files {
		if file.Padding {
			continue
		}
		row, ok := mapping[file.ManifestFileID]
		if !ok || (row.Priority != 0) != file.Selected {
			return false
		}
	}
	return true
}
func btStopped(state string) bool {
	return state == "stoppedDL" || state == "stoppedUP" || state == "stoppedMetaDL"
}
func btPathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func btVerifyRoot(task models.BTTask) error {
	info, err := os.Lstat(task.CanonicalRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrBTPath
	}
	real, err := filepath.EvalSymlinks(task.CanonicalRoot)
	if err != nil || filepath.Clean(real) != filepath.Clean(task.CanonicalRoot) {
		return ErrBTPath
	}
	logical, err := filepath.EvalSymlinks(task.SaveRoot)
	if err != nil || filepath.Clean(logical) != filepath.Clean(real) {
		return ErrBTPath
	}
	return nil
}

// VerifyBTImport is a read-only verifier; it never advances task/file generations.
func (s *BTService) VerifyBTImport(ctx context.Context, task models.BTTask, file models.BTFile) error {
	s.mu.Lock()
	client := s.remote
	ready := s.accepting && client != nil && s.loadedID == task.ConnectionID
	s.mu.Unlock()
	if !ready {
		return ErrBTMaintenance
	}
	remote, _, err := btFindRemote(ctx, client, task)
	if err != nil {
		return err
	}
	if remote.State != task.RemoteState {
		return ErrBTChanged
	}
	rows, err := client.QBFiles(ctx, remote.Hash)
	if err != nil {
		return err
	}
	_, files, err := btLoadTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if !btConfirmedSelection(task, files) {
		return ErrBTSelection
	}
	mapping, err := btMapFiles(files, rows)
	if err != nil {
		return err
	}
	if !btSelectionMatches(files, mapping) {
		return ErrBTChanged
	}
	row, ok := mapping[file.ManifestFileID]
	if !ok || !file.Selected || file.Padding || row.Progress != 1 || row.Priority == 0 {
		return ErrBTChanged
	}
	if err := btVerifyRoot(task); err != nil {
		return err
	}
	path := filepath.Join(task.CanonicalRoot, filepath.FromSlash(file.RelativePath))
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !btPathInside(task.CanonicalRoot, real) {
		return ErrBTPath
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
		return ErrBTPath
	}
	return nil
}
