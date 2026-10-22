package services

import (
	"log"
	"sync"
)

// taskFailureRecorder belongs to one worker lifetime, never executes a retry.
type taskFailureRecorder struct {
	mu      sync.Mutex
	store   *TaskFailureService
	key     string
	runID   uint
	warning string
}

func (r *taskFailureRecorder) configure(store *TaskFailureService, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
	r.key = key
}
func (r *taskFailureRecorder) begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store == nil || r.runID != 0 {
		return
	}
	id, err := r.store.Begin(r.key)
	if err != nil {
		r.warning = "失败诊断记录无法创建"
		log.Printf("task failure begin: %v", err)
		return
	}
	r.runID = id
	r.warning = ""
}
func (r *taskFailureRecorder) record(videoID uint, source, code, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store == nil {
		return
	}
	if r.runID == 0 {
		r.warning = "失败诊断记录不完整"
		return
	}
	if err := r.store.Record(r.runID, r.key, videoID, "", source, code, message); err != nil {
		r.warning = "失败诊断记录不完整"
		log.Printf("task failure record: %v", err)
	}
}
func (r *taskFailureRecorder) finish(cancelled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store == nil || r.runID == 0 {
		return
	}
	state := "completed"
	if cancelled {
		state = "cancelled"
	}
	if err := r.store.Finish(r.runID, state); err != nil {
		r.warning = "失败诊断记录未能完整结束"
		log.Printf("task failure finish: %v", err)
	}
	r.runID = 0
}
func (r *taskFailureRecorder) Warning() string { r.mu.Lock(); defer r.mu.Unlock(); return r.warning }

func (r *taskFailureRecorder) notice(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warning == "" {
		r.warning = message
	}
}
