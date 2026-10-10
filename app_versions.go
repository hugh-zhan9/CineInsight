package main

import (
	"context"
	"fmt"
	"time"
	"video-master/services"
)

// 多版本聚合（D-MW-VERSIONS）的 Wails 接线。数据库维护或切换期间直接拒绝，其余规则在 VersionGroupService。
func (a *App) versionGroupContext() (context.Context, context.CancelFunc, error) {
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, nil, fmt.Errorf("%s", reason)
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 30*time.Second)
	return ctx, cancel, nil
}

func versionGroupCall[T any](a *App, operation func(context.Context, *services.VersionGroupService) (T, error)) (T, error) {
	ctx, cancel, err := a.versionGroupContext()
	if err != nil {
		var zero T
		return zero, err
	}
	defer cancel()
	return operation(ctx, services.NewVersionGroupService())
}

// CreateVersionGroup 用选中的 2–20 个视频建组，传入顺序即版本顺序（第一个是主版本）。
func (a *App) CreateVersionGroup(videoIDs []uint, title string) (*services.VersionGroupDetail, error) {
	return versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (*services.VersionGroupDetail, error) {
		return s.Create(ctx, videoIDs, title)
	})
}

func (a *App) GetVersionGroup(groupID uint) (*services.VersionGroupDetail, error) {
	return versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (*services.VersionGroupDetail, error) {
		return s.Get(ctx, groupID)
	})
}

func (a *App) ListVersionGroups(cursorID uint, limit int) (*services.VersionGroupPage, error) {
	return versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (*services.VersionGroupPage, error) {
		return s.List(ctx, cursorID, limit)
	})
}

func (a *App) AddVersionMembers(groupID uint, expectedRevision int64, videoIDs []uint) (*services.VersionGroupDetail, error) {
	return versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (*services.VersionGroupDetail, error) {
		return s.AddMembers(ctx, groupID, expectedRevision, videoIDs)
	})
}

// RemoveVersionMember 只移出关系、不删文件；组因此不足两个成员而解散时返回 null。
func (a *App) RemoveVersionMember(groupID uint, expectedRevision int64, videoID uint) (*services.VersionGroupDetail, error) {
	return versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (*services.VersionGroupDetail, error) {
		return s.RemoveMember(ctx, groupID, expectedRevision, videoID)
	})
}

func (a *App) ReorderVersionMembers(groupID uint, expectedRevision int64, videoIDs []uint) (*services.VersionGroupDetail, error) {
	return versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (*services.VersionGroupDetail, error) {
		return s.Reorder(ctx, groupID, expectedRevision, videoIDs)
	})
}

func (a *App) UpdateVersionGroup(groupID uint, expectedRevision int64, title string, labels map[uint]string) (*services.VersionGroupDetail, error) {
	return versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (*services.VersionGroupDetail, error) {
		return s.Update(ctx, groupID, expectedRevision, title, labels)
	})
}

func (a *App) DissolveVersionGroup(groupID uint, expectedRevision int64) error {
	_, err := versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (struct{}, error) {
		return struct{}{}, s.Dissolve(ctx, groupID, expectedRevision)
	})
	return err
}

func (a *App) ListVersionGroupSuggestions(limit int) ([]services.VersionGroupSuggestion, error) {
	return versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) ([]services.VersionGroupSuggestion, error) {
		return s.ListSuggestions(ctx, limit)
	})
}

func (a *App) DismissVersionGroupSuggestion(videoIDs []uint) error {
	_, err := versionGroupCall(a, func(ctx context.Context, s *services.VersionGroupService) (struct{}, error) {
		return struct{}{}, s.DismissSuggestion(ctx, videoIDs)
	})
	return err
}
