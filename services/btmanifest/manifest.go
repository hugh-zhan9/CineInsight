// Package btmanifest parses bounded torrent metadata without network or filesystem access.
package btmanifest

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const MaxBytes = 16 << 20
const MaxFiles = 10000
const MaxDepth = 64

var ErrInvalid = errors.New("torrent_invalid: unsafe or inconsistent torrent metadata")

// File IDs are stable manifest identities, never qBittorrent file indexes.
// Path is the protocol path relative to the original-layout save root.
// Padding files appear in the manifest but Selectable is always false for them.
// Path may be empty only for a BEP 47 padding entry that omitted its path.
type File struct {
	ID         string
	Path       string
	Size       int64
	Padding    bool
	Selectable bool
}

// Manifest uses full SHA1/SHA256 hex info hashes and a SHA256 digest of all input
// bytes. V2 paths come directly from file tree (name is advisory); V1 multi-file
// paths include name. Callers must not blindly prepend Name again.
type Manifest struct {
	Name                           string
	Files                          []File
	TotalSize                      int64
	InfoHashV1, InfoHashV2, Digest string
}

// Parse validates the complete bencoding, protocol file layout, hashes and path
// namespace. Expanded file paths together are also limited to MaxBytes. Identical same-sized padding paths may repeat (BEP 47), but
// padding may not collide with real files/directories. TotalSize includes padding.
// Errors never include metadata text (which can contain passkeys).
func Parse(data []byte) (Manifest, error) {
	m, err := parse(data)
	if err != nil {
		return Manifest{}, ErrInvalid
	}
	return m, nil
}
func parse(data []byte) (Manifest, error) {
	m := Manifest{}
	if len(data) == 0 || len(data) > MaxBytes {
		return m, ErrInvalid
	}
	d := decoder{data: data}
	root, err := d.value(0)
	if err != nil || d.pos != len(data) || root.kind != 'd' {
		return m, ErrInvalid
	}
	info := field(root, "info")
	if info == nil || info.kind != 'd' {
		return m, ErrInvalid
	}
	name, ok := text(field(info, "name"))
	if !ok || name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return m, ErrInvalid
	}
	m.Name = name
	if alt := field(info, "name.utf-8"); alt != nil {
		value, valid := text(alt)
		if !valid || value != name {
			return m, ErrInvalid
		}
	}
	piece, ok := integer(field(info, "piece length"))
	if !ok || piece <= 0 {
		return m, ErrInvalid
	}
	versionNode := field(info, "meta version")
	v2 := versionNode != nil
	if v2 {
		v, ok := integer(versionNode)
		if !ok || v != 2 || piece < 16384 || piece&(piece-1) != 0 {
			return m, ErrInvalid
		}
	}
	v1 := field(info, "pieces") != nil || field(info, "files") != nil || field(info, "length") != nil
	if v1 && !component(name) {
		return m, ErrInvalid
	}
	if !v2 && !v1 {
		return m, ErrInvalid
	}
	var a, b []File
	if v1 {
		a, err = v1Files(info, name)
		if err != nil {
			return m, err
		}
		total, err := validatePaths(a)
		if err != nil {
			return m, err
		}
		pieces := field(info, "pieces")
		if pieces == nil || pieces.kind != 's' || len(pieces.text)%20 != 0 {
			return m, ErrInvalid
		}
		count := total / piece
		if total%piece != 0 {
			count++
		}
		if int64(len(pieces.text)/20) != count {
			return m, ErrInvalid
		}
		sum := sha1.Sum(data[info.start:info.end])
		m.InfoHashV1 = hex.EncodeToString(sum[:])
	}
	if v2 {
		tree := field(info, "file tree")
		if tree == nil || tree.kind != 'd' || field(tree, "") != nil {
			return m, ErrInvalid
		}
		layers := field(root, "piece layers")
		if layers == nil || layers.kind != 'd' {
			return m, ErrInvalid
		}
		proofs := pieceLayers{piece: piece, layers: layers, validated: map[string]bool{}, verify: verifyLayer}
		budget := pathBudget(0)
		if err = walkTree(tree, nil, &proofs, &budget, &b); err != nil {
			return m, err
		}
		if len(proofs.validated) != len(layers.dict) {
			return m, ErrInvalid
		}
		if _, err = validatePaths(b); err != nil {
			return m, err
		}
		sum := sha256.Sum256(data[info.start:info.end])
		m.InfoHashV2 = hex.EncodeToString(sum[:])
	} else if field(info, "file tree") != nil || field(root, "piece layers") != nil {
		return m, ErrInvalid
	}
	if v1 && v2 {
		root := ""
		if field(info, "files") != nil {
			root = name + "/"
		}
		if err = hybrid(a, b, piece, root); err != nil {
			return m, err
		}
		m.Files = a
	} else if v1 {
		m.Files = a
	} else {
		m.Files = b
	}
	m.TotalSize, err = validatePaths(m.Files)
	if err != nil {
		return m, err
	}
	for i := range m.Files {
		f := &m.Files[i]
		sum := sha256.Sum256([]byte(strconv.Itoa(i) + "\x00" + f.Path))
		f.ID = hex.EncodeToString(sum[:])
		f.Selectable = !f.Padding
	}
	sum := sha256.Sum256(data)
	m.Digest = hex.EncodeToString(sum[:])
	return m, nil
}
func component(s string) bool {
	return s != "" && s != "." && s != ".." && utf8.ValidString(s) && !strings.ContainsAny(s, "/\\:\x00")
}
func fileProps(n *node, path string) (File, error) {
	if n == nil || n.kind != 'd' || field(n, "symlink path") != nil {
		return File{}, ErrInvalid
	}
	size, ok := integer(field(n, "length"))
	if !ok || size < 0 {
		return File{}, ErrInvalid
	}
	attr := ""
	if a := field(n, "attr"); a != nil {
		var ok bool
		attr, ok = text(a)
		if !ok {
			return File{}, ErrInvalid
		}
	}
	if strings.Contains(attr, "l") {
		return File{}, ErrInvalid
	}
	return File{Path: path, Size: size, Padding: strings.Contains(attr, "p")}, nil
}
func v1Files(info *node, name string) ([]File, error) {
	list := field(info, "files")
	length := field(info, "length")
	if (list == nil) == (length == nil) {
		return nil, ErrInvalid
	}
	if list == nil {
		f, err := fileProps(info, name)
		return []File{f}, err
	}
	if list.kind != 'l' || len(list.list) == 0 || len(list.list) > MaxFiles {
		return nil, ErrInvalid
	}
	result := make([]File, 0, len(list.list))
	budget := pathBudget(0)
	for _, entry := range list.list {
		padding, err := fileProps(entry, "")
		if err != nil {
			return nil, err
		}
		if field(entry, "path") == nil && padding.Padding {
			if field(entry, "path.utf-8") != nil {
				return nil, ErrInvalid
			}
			result = append(result, padding)
			continue
		}

		path := field(entry, "path")
		if path == nil || path.kind != 'l' || len(path.list) == 0 || len(path.list)+1 > MaxDepth {
			return nil, ErrInvalid
		}
		parts := []string{name}
		for _, part := range path.list {
			s, ok := text(part)
			if !ok || !component(s) {
				return nil, ErrInvalid
			}
			parts = append(parts, s)
		}
		// Reject alternate UTF-8 aliases that could cause the client to choose another path.
		if alt := field(entry, "path.utf-8"); alt != nil {
			if alt.kind != 'l' || len(alt.list) != len(path.list) {
				return nil, ErrInvalid
			}
			for i, v := range alt.list {
				s, ok := text(v)
				if !ok || s != parts[i+1] {
					return nil, ErrInvalid
				}
			}
		}
		pathText, err := budget.join(parts)
		if err != nil {
			return nil, err
		}
		f, err := fileProps(entry, pathText)
		if err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	if alt := field(info, "name.utf-8"); alt != nil {
		s, ok := text(alt)
		if !ok || s != name {
			return nil, ErrInvalid
		}
	}
	return result, nil
}

// Bound expanded paths as well as input bytes: shared prefixes must not turn a
// small file tree into unbounded duplicated strings.
type pathBudget int

func (b *pathBudget) join(parts []string) (string, error) {
	size := len(parts) - 1
	for _, part := range parts {
		if len(part) > MaxBytes-size {
			return "", ErrInvalid
		}
		size += len(part)
	}
	if size > MaxBytes-int(*b) {
		return "", ErrInvalid
	}
	*b += pathBudget(size)
	return strings.Join(parts, "/"), nil
}

func walkTree(n *node, parts []string, proofs *pieceLayers, budget *pathBudget, result *[]File) error {
	if n.kind != 'd' || len(n.keys) == 0 {
		return ErrInvalid
	}
	if leaf := field(n, ""); leaf != nil {
		if len(parts) == 0 || len(n.keys) != 1 || len(*result) >= MaxFiles {
			return ErrInvalid
		}
		pathText, err := budget.join(parts)
		if err != nil {
			return err
		}
		f, err := fileProps(leaf, pathText)
		if err != nil {
			return err
		}
		hash := field(leaf, "pieces root")
		if f.Size == 0 {
			if hash != nil {
				return ErrInvalid
			}
		} else {
			if hash == nil || hash.kind != 's' || len(hash.text) != 32 {
				return ErrInvalid
			}
			if f.Size > proofs.piece {
				if err := proofs.check(hash.text, f.Size); err != nil {
					return err
				}
			}
		}
		*result = append(*result, f)
		return nil
	}
	for _, key := range n.keys {
		if !component(key) || len(parts) >= MaxDepth {
			return ErrInvalid
		}
		child := append(append([]string{}, parts...), key)
		if err := walkTree(n.dict[key], child, proofs, budget, result); err != nil {
			return err
		}
	}
	return nil
}

// pieceLayers validates a shared Merkle layer at most once per parse. File sizes
// remain independently checked even when several files use the same root.
type pieceLayers struct {
	piece     int64
	layers    *node
	validated map[string]bool
	verify    func([]byte, []byte, int64) bool
}

func (p *pieceLayers) check(root []byte, size int64) error {
	layer := field(p.layers, string(root))
	if layer == nil || layer.kind != 's' {
		return ErrInvalid
	}
	count := size / p.piece
	if size%p.piece != 0 {
		count++
	}
	if int64(len(layer.text))/32 != count || len(layer.text)%32 != 0 {
		return ErrInvalid
	}
	key := string(root)
	if !p.validated[key] {
		if !p.verify(layer.text, root, p.piece) {
			return ErrInvalid
		}
		p.validated[key] = true
	}
	return nil
}

func verifyLayer(data, root []byte, piece int64) bool {
	hashes := make([][32]byte, len(data)/32)
	for i := range hashes {
		copy(hashes[i][:], data[i*32:(i+1)*32])
	}
	zero := [32]byte{}
	for n := piece / 16384; n > 1; n /= 2 {
		var pair [64]byte
		copy(pair[:32], zero[:])
		copy(pair[32:], zero[:])
		zero = sha256.Sum256(pair[:])
	}
	for len(hashes) > 1 {
		if len(hashes)%2 != 0 {
			hashes = append(hashes, zero)
		}
		next := make([][32]byte, len(hashes)/2)
		for i := range next {
			var pair [64]byte
			copy(pair[:32], hashes[i*2][:])
			copy(pair[32:], hashes[i*2+1][:])
			next[i] = sha256.Sum256(pair[:])
		}
		hashes = next
		var pair [64]byte
		copy(pair[:32], zero[:])
		copy(pair[32:], zero[:])
		zero = sha256.Sum256(pair[:])
	}
	return len(hashes) == 1 && string(hashes[0][:]) == string(root)
}

// pathNode stores components, never repeated full directory prefixes. Its total
// string storage is proportional to the already-bounded expanded file paths.
type pathNode struct {
	raw      string
	children map[string]*pathNode
	file     *pathFile
}
type pathFile struct {
	size    int64
	padding bool
}

func (root *pathNode) insert(f File) error {
	parts := strings.Split(f.Path, "/")
	if len(parts) > MaxDepth {
		return ErrInvalid
	}
	fold := cases.Fold()
	current := root
	for i, part := range parts {
		if !component(part) {
			return ErrInvalid
		}
		key := norm.NFC.String(fold.String(norm.NFC.String(part)))
		if current.children == nil {
			current.children = map[string]*pathNode{}
		}
		next := current.children[key]
		if next == nil {
			next = &pathNode{raw: part}
			current.children[key] = next
		}
		last := i == len(parts)-1
		if last {
			if len(next.children) > 0 {
				return ErrInvalid
			}
			if prior := next.file; prior != nil {
				if !(prior.padding && f.Padding && prior.size == f.Size) {
					return ErrInvalid
				}
			} else {
				next.file = &pathFile{size: f.Size, padding: f.Padding}
			}
		} else {
			if next.file != nil || next.raw != part {
				return ErrInvalid
			}
		}
		current = next
	}
	return nil
}
func validatePaths(files []File) (int64, error) {
	if len(files) == 0 || len(files) > MaxFiles {
		return 0, ErrInvalid
	}
	var namespace pathNode
	var total int64
	for _, f := range files {
		if f.Size < 0 || f.Size > math.MaxInt64-total {
			return 0, ErrInvalid
		}
		total += f.Size
		if f.Padding && f.Path == "" {
			continue
		}
		if err := namespace.insert(f); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func hybrid(a, b []File, piece int64, v1Root string) error {
	var offset int64
	index := 0
	for _, f := range a {
		if f.Padding {
			if offset%piece == 0 || f.Size != piece-offset%piece {
				return ErrInvalid
			}
			offset += f.Size
			continue
		}
		// V1 files include the name directory; the v2 tree describes paths
		// below it. Single-file length torrents have no directory to remove.
		relative := strings.TrimPrefix(f.Path, v1Root)
		if index >= len(b) || b[index].Padding || relative != b[index].Path || f.Size != b[index].Size || (f.Size > 0 && offset%piece != 0) {
			return ErrInvalid
		}
		index++
		offset += f.Size
	}
	if index != len(b) {
		return ErrInvalid
	}
	return nil
}
