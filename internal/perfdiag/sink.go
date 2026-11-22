package perfdiag

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	perfFileLimit   int64 = 1 << 20
	perfRecordLimit       = 4 << 10
)

var localPerf struct {
	mu       sync.Mutex
	enabled  atomic.Bool
	revision atomic.Uint64
	file     *os.File
	path     string
	size     int64
}

// Enabled does not create files and is independent of the standard log sink.
func Enabled() bool { return localPerf.enabled.Load() }

// Configure opens the dedicated private local sink only on explicit opt-in.
// The caller's app directory must already exist. Reconfiguring the same enabled
// destination is a no-op; disabling leaves existing diagnostic files untouched.
func Configure(path string, enabled bool) error {
	localPerf.mu.Lock()
	defer localPerf.mu.Unlock()
	if enabled && path != "" && localPerf.path == path && localPerf.file != nil {
		return nil
	}
	if err := closeLocked(); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if path == "" {
		return errors.New("missing local performance log destination")
	}
	if err := prepareBackup(path + ".1"); err != nil {
		return errors.New("cannot secure local performance log backup")
	}
	f, size, err := privateLogFile(path)
	if err != nil {
		return errors.New("cannot open local performance log")
	}
	// Bound migrated files as well as new writes; do not move an oversized
	// previous file into the bounded backup slot.
	if size > perfFileLimit {
		if err := f.Truncate(0); err != nil {
			_ = f.Close()
			return errors.New("cannot bound local performance log")
		}
		size = 0
	}
	localPerf.file, localPerf.path, localPerf.size = f, path, size
	localPerf.enabled.Store(true)
	return nil
}

func Close() error { return Configure("", false) }

func closeLocked() error {
	localPerf.enabled.Store(false)
	localPerf.revision.Add(1)
	f := localPerf.file
	localPerf.file, localPerf.path, localPerf.size = nil, "", 0
	if f != nil && f.Close() != nil {
		return errors.New("cannot close local performance log")
	}
	return nil
}

// Printf accepts fixed diagnostic formats with sanitized metric values. It
// never writes to log.Printf, stdout/stderr, or a fallback sink. Unreasonably
// large or multiline records are dropped instead of growing the local file.
func Printf(format string, args ...any) {
	revision := localPerf.revision.Load()
	if !Enabled() {
		return
	}
	body := fmt.Sprintf(format, args...)
	if len(body) == 0 || len(body) > perfRecordLimit-40 {
		return
	}
	for _, c := range body {
		if c == '\r' || c == '\n' {
			return
		}
	}
	record := time.Now().UTC().Format(time.RFC3339Nano) + " " + body + "\n"
	localPerf.mu.Lock()
	defer localPerf.mu.Unlock()
	if localPerf.file == nil || revision != localPerf.revision.Load() {
		return
	}
	if localPerf.size+int64(len(record)) > perfFileLimit {
		if err := rotateLocked(); err != nil {
			_ = closeLocked()
			return
		}
	}
	n, err := io.WriteString(localPerf.file, record)
	if err != nil || n != len(record) {
		_ = closeLocked()
		return
	}
	localPerf.size += int64(n)
}

func privateLogFile(path string) (*os.File, int64, error) {
	before, err := os.Lstat(path)
	flags := os.O_WRONLY | os.O_APPEND
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
		before = nil
	} else if err != nil || !before.Mode().IsRegular() {
		return nil, 0, errors.New("not a regular diagnostic file")
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, 0, err
	}
	info, statErr := f.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !info.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(info, current) || (before != nil && !os.SameFile(before, info)) {
		_ = f.Close()
		return nil, 0, errors.New("diagnostic file changed during open")
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

func prepareBackup(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("invalid performance backup file")
	}
	f, size, err := privateLogFile(path)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if size > perfFileLimit {
		return os.Remove(path)
	}
	return nil
}

func rotateLocked() error {
	path := localPerf.path
	active, err := localPerf.file.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(active, current) {
		return errors.New("performance log changed before rotation")
	}
	backup := path + ".1"
	if err := prepareBackup(backup); err != nil {
		return err
	}
	if err := localPerf.file.Close(); err != nil {
		return err
	}
	localPerf.file = nil
	// Explicit removal keeps the path portable on targets where Rename does
	// not replace an existing file. The backup is part of the bounded log.
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	localPerf.file, localPerf.size = f, 0
	return nil
}
