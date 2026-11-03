package services

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"time"
	"video-master/database"
)

var ErrLibraryQueryRequestUsed = errors.New("query_request_used: 这次解析已经处理，请重新发起")

type LibraryQueryRequestState struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}
type libraryQueryRequest struct {
	owner   context.Context
	db      *gorm.DB
	cancel  context.CancelFunc
	status  string
	expires time.Time
}

func validLibraryQueryRequestID(id string) bool {
	_, err := uuid.Parse(id)
	return len(id) == 36 && err == nil
}
func (s *LibraryQueryService) beginRequest(owner context.Context, id string) (context.Context, *libraryQueryRequest, func(), error) {
	if !validLibraryQueryRequestID(id) {
		return nil, nil, nil, ErrLibraryQueryInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if previous := s.requests[id]; previous != nil {
		if previous.status == "cancelled" {
			return nil, nil, nil, context.Canceled
		}
		return nil, nil, nil, ErrLibraryQueryRequestUsed
	}
	if len(s.requests) >= 128 {
		return nil, nil, nil, errors.New("invalid_input: 解析请求过多，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(owner, 30*time.Second)
	request := &libraryQueryRequest{owner: owner, db: database.DB, cancel: cancel, status: "running", expires: time.Now().Add(10 * time.Minute)}
	s.requests[id] = request
	finish := func() {
		s.mu.Lock()
		if request.status == "running" {
			request.status = "failed"
			if ctx.Err() != nil {
				request.status = "cancelled"
			}
		}
		s.mu.Unlock()
		cancel()
	}
	return ctx, request, finish, nil
}

// Cancel keeps a tombstone when it wins admission, so an out-of-order Parse call
// cannot send text after the user has closed that request.
func (s *LibraryQueryService) Cancel(owner context.Context, id string) (*LibraryQueryRequestState, error) {
	if !validLibraryQueryRequestID(id) {
		return nil, ErrLibraryQueryInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	request := s.requests[id]
	if request == nil {
		if len(s.requests) >= 128 {
			return nil, errors.New("invalid_input: 解析请求过多，请稍后重试")
		}
		request = &libraryQueryRequest{owner: owner, db: database.DB, status: "cancelled", expires: time.Now().Add(10 * time.Minute)}
		s.requests[id] = request
	}
	if request.status == "running" {
		request.status = "cancelled"
		request.cancel()
	}
	return &LibraryQueryRequestState{RequestID: id, Status: request.status}, nil
}
