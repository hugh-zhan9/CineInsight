package btmanifest

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func enc(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Itoa(len(x)) + ":" + x
	case int:
		return "i" + strconv.Itoa(x) + "e"
	case int64:
		return "i" + strconv.FormatInt(x, 10) + "e"
	case []any:
		var s strings.Builder
		s.WriteByte('l')
		for _, v := range x {
			s.WriteString(enc(v))
		}
		s.WriteByte('e')
		return s.String()
	case map[string]any:
		keys := []string{}
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var s strings.Builder
		s.WriteByte('d')
		for _, k := range keys {
			s.WriteString(enc(k))
			s.WriteString(enc(x[k]))
		}
		s.WriteByte('e')
		return s.String()
	}
	panic("fixture type")
}
func v1(name string, size int64) map[string]any {
	count := size / 16384
	if size%16384 != 0 {
		count++
	}
	return map[string]any{"name": name, "length": size, "piece length": 16384, "pieces": strings.Repeat("x", int(count)*20)}
}
func wrap(info map[string]any) []byte { return []byte(enc(map[string]any{"info": info})) }
func leaf(size int64) map[string]any {
	n := map[string]any{"length": size}
	if size > 0 {
		n["pieces root"] = strings.Repeat("r", 32)
	}
	return map[string]any{"": n}
}
func v2(tree map[string]any) map[string]any {
	return map[string]any{"name": "display", "meta version": 2, "piece length": 16384, "file tree": tree}
}
func wrap2(info map[string]any) []byte {
	return []byte(enc(map[string]any{"info": info, "piece layers": map[string]any{}}))
}
func mustParse(t *testing.T, raw []byte) Manifest {
	t.Helper()
	m, e := Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func bad(t *testing.T, raw []byte) {
	t.Helper()
	m, e := Parse(raw)
	if e == nil || len(m.Files) > 0 {
		t.Fatalf("unsafe accepted: %.100x", raw)
	}
}
func TestHashesAndV1Layout(t *testing.T) {
	info := v1("电影.mkv", 42)
	raw := wrap(info)
	m := mustParse(t, raw)
	h1 := sha1.Sum([]byte(enc(info)))
	h2 := sha256.Sum256(raw)
	if m.InfoHashV1 != hex.EncodeToString(h1[:]) || m.InfoHashV2 != "" || m.Digest != hex.EncodeToString(h2[:]) || m.Files[0].Path != "电影.mkv" || m.TotalSize != 42 || !m.Files[0].Selectable {
		t.Fatalf("%+v", m)
	}
	info = map[string]any{"name": "series", "files": []any{map[string]any{"length": int64(1), "path": []any{"one.mkv"}}, map[string]any{"length": int64(2), "path": []any{"sub", "two.mkv"}}}, "piece length": 16384, "pieces": strings.Repeat("x", 20)}
	m = mustParse(t, wrap(info))
	if m.Files[0].Path != "series/one.mkv" || m.Files[1].Path != "series/sub/two.mkv" || m.TotalSize != 3 {
		t.Fatalf("%+v", m)
	}
	if m.Files[0].ID == m.Files[1].ID || m.Files[0].ID != mustParse(t, wrap(info)).Files[0].ID {
		t.Fatal("unstable IDs")
	}
}
func TestV2RootlessAndMerkleLayers(t *testing.T) {
	info := v2(map[string]any{"a.mkv": leaf(1), "dir": map[string]any{"b.mkv": leaf(0)}})
	m := mustParse(t, wrap2(info))
	h := sha256.Sum256([]byte(enc(info)))
	if m.InfoHashV2 != hex.EncodeToString(h[:]) || m.InfoHashV1 != "" || m.Files[0].Path != "a.mkv" || m.Files[1].Path != "dir/b.mkv" {
		t.Fatalf("%+v", m)
	}
	a := sha256.Sum256([]byte("first block"))
	b := sha256.Sum256([]byte("second block"))
	pair := append(append([]byte{}, a[:]...), b[:]...)
	root := sha256.Sum256(pair)
	info = v2(map[string]any{"large.mkv": map[string]any{"": map[string]any{"length": int64(32768), "pieces root": string(root[:])}}})
	top := map[string]any{"info": info, "piece layers": map[string]any{string(root[:]): string(pair)}}
	mustParse(t, []byte(enc(top)))
	pair[0] ^= 1
	top["piece layers"] = map[string]any{string(root[:]): string(pair)}
	bad(t, []byte(enc(top)))
	bad(t, wrap2(info))
	bad(t, wrap(info))
}
func TestHybridPaddingAndIdentity(t *testing.T) {
	real := func(name string) any { return map[string]any{"length": int64(1), "path": []any{name}} }
	pad := func() any { return map[string]any{"attr": "p", "length": int64(16383), "path": []any{".pad", "16383"}} }
	info := v2(map[string]any{"a": leaf(1), "b": leaf(1), "c": leaf(1)})
	info["name"] = "series"
	info["files"] = []any{real("a"), pad(), real("b"), pad(), real("c")}
	info["pieces"] = strings.Repeat("x", 60)
	m := mustParse(t, wrap2(info))
	if m.InfoHashV1 == "" || m.InfoHashV2 == "" || len(m.Files) != 5 || m.Files[1].Selectable || !m.Files[1].Padding || m.Files[1].ID == m.Files[3].ID {
		t.Fatalf("%+v", m)
	}
	info["files"].([]any)[2] = real("wrong")
	bad(t, wrap2(info))
	info["files"] = []any{real("a"), real("b"), real("c")}
	info["pieces"] = strings.Repeat("x", 20)
	bad(t, wrap2(info))
}
func TestUnsafePathsAndAliases(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/abs", "a/b", "C:\\x", "a:b", "a\x00b", string([]byte{0xff})} {
		bad(t, wrap(v1(name, 1)))
	}
	for _, paths := range [][]string{{"a", "A"}, {"é", "e\u0301"}, {"a", "a/b"}, {"a/b", "a"}, {"A/b", "a/B"}, {"A/one", "a/two"}} {
		var files []any
		for _, path := range paths {
			parts := []any{}
			for _, part := range strings.Split(path, "/") {
				parts = append(parts, part)
			}
			files = append(files, map[string]any{"length": int64(1), "path": parts})
		}
		info := map[string]any{"name": "root", "files": files, "piece length": 16384, "pieces": strings.Repeat("x", 20)}
		bad(t, wrap(info))
	}
	info := v1("ok", 1)
	info["name.utf-8"] = "elsewhere"
	bad(t, wrap(info))
	info = v1("ok", 1)
	info["attr"] = "l"
	info["symlink path"] = []any{"target"}
	bad(t, wrap(info))
	info = v2(map[string]any{"a": map[string]any{"": map[string]any{"length": int64(0)}, "b": leaf(0)}})
	bad(t, wrap2(info))
}
func TestMalformedAndBounds(t *testing.T) {
	for _, raw := range []string{"", "degarbage", "d4:infoi1ee", "d1:ai1e1:ai2ee", "d1:bi1e1:ai2ee", "i01e", "i-0e", "i+1e", "01:x", "999999999999999999:x", "l", strings.Repeat("l", 72) + strings.Repeat("e", 72)} {
		bad(t, []byte(raw))
	}
	bad(t, make([]byte, MaxBytes+1))
	info := v1("x", 1)
	info["pieces"] = "x"
	bad(t, wrap(info))
	info = v1("x", 1)
	info["files"] = []any{}
	bad(t, wrap(info))
	info = v2(map[string]any{"x": leaf(1)})
	info["meta version"] = 3
	bad(t, wrap2(info))
	info = v2(map[string]any{"x": leaf(1)})
	info["piece length"] = 17000
	bad(t, wrap2(info))
	for _, n := range []int{MaxFiles, MaxFiles + 1} {
		files := []any{}
		for i := 0; i < n; i++ {
			files = append(files, map[string]any{"length": int64(0), "path": []any{fmt.Sprint(i)}})
		}
		info := map[string]any{"name": "root", "files": files, "piece length": 16384, "pieces": ""}
		if n == MaxFiles {
			mustParse(t, wrap(info))
		} else {
			bad(t, wrap(info))
		}
	}
	huge := map[string]any{"name": "r", "files": []any{map[string]any{"length": int64(math.MaxInt64), "path": []any{"a"}}, map[string]any{"length": int64(1), "path": []any{"b"}}}, "piece length": 16384, "pieces": ""}
	bad(t, wrap(huge))
}
func FuzzParse(f *testing.F) {
	f.Add(wrap(v1("a", 1)))
	f.Add(wrap2(v2(map[string]any{"a": leaf(1)})))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Parse(data)
		if err == nil && (len(m.Files) == 0 || len(m.Files) > MaxFiles) {
			t.Fatal("invalid manifest")
		}
	})
}

func TestPathDepthBoundary(t *testing.T) {
	for _, count := range []int{64, 65} {
		tree := leaf(0)
		for i := 0; i < count; i++ {
			tree = map[string]any{fmt.Sprint(i): tree}
		}
		if count == 64 {
			mustParse(t, wrap2(v2(tree)))
		} else {
			bad(t, wrap2(v2(tree)))
		}
	}
}

func TestV2NameIsDisplayText(t *testing.T) {
	for _, name := range []string{"Movie: Director's Cut", "Season / UHD", ".."} {
		info := v2(map[string]any{"safe.mkv": leaf(1)})
		info["name"] = name
		m := mustParse(t, wrap2(info))
		if m.Name != name || m.Files[0].Path != "safe.mkv" {
			t.Fatalf("display name changed path: %+v", m)
		}
	}
	for _, name := range []string{"", "bad\x00name", string([]byte{0xff})} {
		info := v2(map[string]any{"safe.mkv": leaf(1)})
		info["name"] = name
		bad(t, wrap2(info))
	}
}
func TestSharedPieceLayerVerifiedOnce(t *testing.T) {
	a := sha256.Sum256([]byte("first block"))
	b := sha256.Sum256([]byte("second block"))
	layer := append(append([]byte{}, a[:]...), b[:]...)
	root := sha256.Sum256(layer)
	tree := map[string]any{}
	for i := 0; i < MaxFiles; i++ {
		tree[fmt.Sprintf("f%05d", i)] = map[string]any{"": map[string]any{"length": int64(32768), "pieces root": string(root[:])}}
	}
	raw := []byte(enc(tree))
	d := decoder{data: raw}
	n, err := d.value(0)
	if err != nil {
		t.Fatal(err)
	}
	layersRaw := []byte(enc(map[string]any{string(root[:]): string(layer)}))
	ld := decoder{data: layersRaw}
	layers, err := ld.value(0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	proofs := pieceLayers{piece: 16384, layers: layers, validated: map[string]bool{}, verify: func(data, root []byte, piece int64) bool { calls++; return verifyLayer(data, root, piece) }}
	var files []File
	budget := pathBudget(0)
	if err := walkTree(n, nil, &proofs, &budget, &files); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(files) != MaxFiles {
		t.Fatalf("shared layer calls=%d files=%d", calls, len(files))
	}
	if err := proofs.check(root[:], int64(49152)); err == nil {
		t.Fatal("cached root bypassed per-file size check")
	}
	if calls != 1 {
		t.Fatal("bad size triggered unnecessary Merkle work")
	}
}

func TestPaddingMayOmitPath(t *testing.T) {
	real := func(name string) any { return map[string]any{"length": int64(1), "path": []any{name}} }
	padding := func() any { return map[string]any{"attr": "p", "length": int64(16383)} }
	info := v2(map[string]any{"a": leaf(1), "b": leaf(1), "c": leaf(1)})
	info["name"] = "series"
	info["files"] = []any{real("a"), padding(), real("b"), padding(), real("c")}
	info["pieces"] = strings.Repeat("x", 60)
	m := mustParse(t, wrap2(info))
	if m.TotalSize != 32769 || len(m.Files) != 5 || m.Files[1].Path != "" || m.Files[3].Path != "" || m.Files[1].Selectable || m.Files[1].ID == m.Files[3].ID {
		t.Fatalf("bad omitted padding: %+v", m)
	}
	delete(info, "meta version")
	delete(info, "file tree")
	mustParse(t, wrap(info))
	info["files"].([]any)[1] = map[string]any{"length": int64(16383)}
	bad(t, wrap(info))
	info["files"].([]any)[1] = map[string]any{"attr": "p", "length": int64(16383), "path": []any{}}
	bad(t, wrap(info))
}

func TestExpandedPathBudget(t *testing.T) {
	prefix := strings.Repeat("p", MaxBytes/2)
	info := v2(map[string]any{prefix: map[string]any{"a": leaf(0), "b": leaf(0)}})
	raw := wrap2(info)
	if len(raw) >= MaxBytes {
		t.Fatal("fixture must fit input limit")
	}
	bad(t, raw)
	info = map[string]any{"name": prefix, "files": []any{map[string]any{"length": int64(0), "path": []any{"a"}}, map[string]any{"length": int64(0), "path": []any{"b"}}}, "piece length": 16384, "pieces": ""}
	raw = wrap(info)
	if len(raw) >= MaxBytes {
		t.Fatal("fixture must fit input limit")
	}
	bad(t, raw)
	budget := pathBudget(0)
	if _, err := budget.join([]string{strings.Repeat("x", MaxBytes)}); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.join([]string{"x"}); err == nil {
		t.Fatal("aggregate overflow accepted")
	}
}

// Use a small long-prefix fixture and count retained component bytes. This
// catches depth-multiplied prefix storage without a large allocation stress test.
func TestDirectoryPrefixStorageIsLinear(t *testing.T) {
	parts := make([]string, MaxDepth)
	parts[0] = strings.Repeat("LongPrefix", 800)
	for i := 1; i < len(parts); i++ {
		parts[i] = fmt.Sprintf("d%02d", i)
	}
	var namespace pathNode
	if err := namespace.insert(File{Path: strings.Join(parts, "/"), Size: 1}); err != nil {
		t.Fatal(err)
	}
	count, stored := 0, 0
	var visit func(*pathNode)
	visit = func(n *pathNode) {
		for key, child := range n.children {
			count++
			stored += len(key) + len(child.raw)
			visit(child)
		}
	}
	visit(&namespace)
	want := 0
	for _, part := range parts {
		want += 2 * len(part)
	}
	if count != MaxDepth || stored != want {
		t.Fatalf("prefix storage amplified: nodes=%d bytes=%d want=%d", count, stored, want)
	}
	// Sharing the directory chain adds only the new leaf, not another prefix.
	parts[len(parts)-1] = "second.mkv"
	if err := namespace.insert(File{Path: strings.Join(parts, "/"), Size: 1}); err != nil {
		t.Fatal(err)
	}
	count, stored = 0, 0
	visit(&namespace)
	if count != MaxDepth+1 || stored != want+2*len("second.mkv") {
		t.Fatalf("shared directory storage amplified: nodes=%d bytes=%d", count, stored)
	}
}

// Hybrid paths share the v1 name root in storage; the v2 tree describes paths
// below that root. A v1 length torrent, unlike a files list, has no such prefix.
func TestHybridRelativePathsAndIdentity(t *testing.T) {
	for _, single := range []bool{false, true} {
		for _, size := range []int64{0, 1} {
			info := v2(map[string]any{"movie.mkv": leaf(size)})
			expected := "season/movie.mkv"
			info["name"] = "season"
			if single {
				info["name"] = "movie.mkv"
				info["length"] = size
				expected = "movie.mkv"
			} else {
				info["files"] = []any{map[string]any{"length": size, "path": []any{"movie.mkv"}}}
			}
			info["pieces"] = ""
			if size > 0 {
				info["pieces"] = strings.Repeat("x", 20)
			}
			m := mustParse(t, wrap2(info))
			h1 := sha1.Sum([]byte(enc(info)))
			h2 := sha256.Sum256([]byte(enc(info)))
			id := sha256.Sum256([]byte("0\x00" + expected))
			if len(m.Files) != 1 || m.Files[0].Path != expected || m.Files[0].ID != hex.EncodeToString(id[:]) || m.InfoHashV1 != hex.EncodeToString(h1[:]) || m.InfoHashV2 != hex.EncodeToString(h2[:]) {
				t.Fatalf("single=%v size=%d manifest=%+v", single, size, m)
			}
			info["file tree"] = map[string]any{info["name"].(string): map[string]any{"movie.mkv": leaf(size)}}
			bad(t, wrap2(info)) // duplicate name directory is a different file
		}
	}
}

func TestHybridNestedNamesAndInconsistencies(t *testing.T) {
	fixture := func() map[string]any {
		info := v2(map[string]any{"a": leaf(0), "dir": map[string]any{"b": leaf(1)}, "z": leaf(0)})
		info["name"] = "season"
		info["files"] = []any{
			map[string]any{"length": int64(0), "path": []any{"a"}},
			map[string]any{"length": int64(1), "path": []any{"dir", "b"}},
			map[string]any{"length": int64(0), "path": []any{"z"}},
		}
		info["pieces"] = strings.Repeat("x", 20)
		return info
	}
	m := mustParse(t, wrap2(fixture()))
	if m.Files[1].Path != "season/dir/b" {
		t.Fatalf("unexpected path %+v", m.Files)
	}
	for _, mutate := range []func(map[string]any){
		func(i map[string]any) { i["file tree"] = map[string]any{"season": i["file tree"]} },
		func(i map[string]any) { i["file tree"].(map[string]any)["dir"] = map[string]any{"other": leaf(1)} },
		func(i map[string]any) { i["file tree"].(map[string]any)["dir"] = map[string]any{"b": leaf(2)} },
		func(i map[string]any) { f := i["files"].([]any); f[0], f[1] = f[1], f[0] },
	} {
		info := fixture()
		mutate(info)
		bad(t, wrap2(info))
	}
	// A directory whose name happens to equal the torrent name is retained once.
	info := v2(map[string]any{"season": map[string]any{"movie": leaf(1)}})
	info["name"] = "season"
	info["files"] = []any{map[string]any{"length": int64(1), "path": []any{"season", "movie"}}}
	info["pieces"] = strings.Repeat("x", 20)
	m = mustParse(t, wrap2(info))
	if m.Files[0].Path != "season/season/movie" {
		t.Fatal(m.Files)
	}
}
