package services

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
)

const (
	// cleanupStatWorkers 是清理分析首轮 os.Stat 的并发上限。外置盘、网络盘上单次 stat
	// 可达毫秒级，万级文件串行 stat 是分析开头的主要等待。
	cleanupStatWorkers = 8
	// cleanupStatChunk 是一批并行 stat 的条数：按批取用，进度事件与取消检查保持原有节奏，
	// 也不会在开始时把整库路径一次性 stat 完。批次取小：批内后面的条目要等前面的处理完
	// （可能含 ffprobe 补探测）才被使用，批越大 stat 结果越旧。
	cleanupStatChunk = 64
)

type cleanupStatResult struct {
	info os.FileInfo
	err  error
}

// statCleanupPaths 以最多 cleanupStatWorkers 个并发 os.Stat 读取 paths，结果与输入同序。
// ctx 取消后不再发起新的 stat，未读到的条目保持零值，调用方须在取用后检查 ctx.Err()。
func statCleanupPaths(ctx context.Context, paths []string) []cleanupStatResult {
	results := make([]cleanupStatResult, len(paths))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(cleanupStatWorkers, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				index := int(next.Add(1) - 1)
				if index >= len(paths) || ctx.Err() != nil {
					return
				}
				info, err := os.Stat(paths[index])
				results[index] = cleanupStatResult{info: info, err: err}
			}
		}()
	}
	wg.Wait()
	return results
}

// cleanupStatBatcher 按需分批并行 stat：第一次取某个下标时，把从它开始的一批一起读完，
// 之后按原顺序逐条交给调用方。结果与逐条串行 stat 相同，唯一差别是 stat 时刻提前到所在批次
// 开始时：分析期间文件被改动，批内条目看到的是本批开始时的状态（与首轮快照复用同一口径）。
type cleanupStatBatcher struct {
	ctx     context.Context
	paths   []string
	start   int
	results []cleanupStatResult
}

func newCleanupStatBatcher(ctx context.Context, paths []string) *cleanupStatBatcher {
	return &cleanupStatBatcher{ctx: ctx, paths: paths}
}

// stat 返回第 index 条路径的 os.Stat 结果。ctx 已取消时结果可能是零值（info 与 err 都为 nil），
// 调用方必须先检查 ctx.Err() 再使用。
func (b *cleanupStatBatcher) stat(index int) (os.FileInfo, error) {
	if index < b.start || index >= b.start+len(b.results) {
		b.start = index
		b.results = statCleanupPaths(b.ctx, b.paths[index:min(len(b.paths), index+cleanupStatChunk)])
	}
	result := b.results[index-b.start]
	return result.info, result.err
}

// cleanupFileSnapshot 是视频清理首轮 os.Stat 的结果。近似重复与截取片段两步复用它，
// 不再对同一批文件各 stat 一遍（外置盘上这是两轮万级的同步 IO）。
type cleanupFileSnapshot struct {
	path      string
	statOK    bool
	regular   bool
	size      int64
	modTimeNS int64
}

// cleanupFileSnapshots 按视频 ID 记录首轮快照。
type cleanupFileSnapshots map[uint]cleanupFileSnapshot

func (s cleanupFileSnapshots) record(id uint, path string, info os.FileInfo, err error) {
	if s == nil {
		return
	}
	if err != nil {
		s[id] = cleanupFileSnapshot{path: path}
		return
	}
	s[id] = cleanupFileSnapshot{path: path, statOK: true, regular: info.Mode().IsRegular(), size: info.Size(), modTimeNS: info.ModTime().UnixNano()}
}

// regularFile 报告视频文件是否是读得到的普通文件，并给出大小与 mtime（纳秒），
// 判据与原先各步自己 os.Stat 时相同：stat 失败或不是普通文件都算不可用。
//
// 唯一的行为差别：用的是首轮的 stat 结果，分析进行期间文件被改动、删除或新出现，
// 这两步看到的是首轮时的状态（原先是各自 stat 那一刻的状态）。首轮没有记录的视频
// （首轮之后才入库或改了路径，或调用方没有首轮）仍当场 stat，与原先一致。
func (s cleanupFileSnapshots) regularFile(id uint, path string) (int64, int64, bool) {
	if entry, found := s[id]; found && entry.path == path {
		return entry.size, entry.modTimeNS, entry.statOK && entry.regular
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, 0, false
	}
	return info.Size(), info.ModTime().UnixNano(), true
}
