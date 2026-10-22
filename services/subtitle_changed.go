package services

import "sync"

var subtitleChanges struct {
	sync.RWMutex
	listener func()
}

// SetSubtitleChangedListener observes successful replacements, including shared same-name SRT files.
// The callback only signals consumers to reload; it must not block or re-enter the writer.
func SetSubtitleChangedListener(listener func()) {
	subtitleChanges.Lock()
	subtitleChanges.listener = listener
	subtitleChanges.Unlock()
}
func notifySubtitleChanged() {
	subtitleChanges.RLock()
	listener := subtitleChanges.listener
	subtitleChanges.RUnlock()
	if listener != nil {
		listener()
	}
}
