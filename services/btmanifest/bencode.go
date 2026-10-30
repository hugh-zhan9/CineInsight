package btmanifest

import (
	"bytes"
	"strconv"
)

type node struct {
	kind       byte
	text       []byte
	number     int64
	list       []*node
	dict       map[string]*node
	keys       []string
	start, end int
}
type decoder struct {
	data       []byte
	pos, nodes int
}

func (d *decoder) value(depth int) (*node, error) {
	if depth > 70 || d.pos >= len(d.data) || d.nodes >= 150000 {
		return nil, ErrInvalid
	}
	d.nodes++
	n := &node{start: d.pos}
	ch := d.data[d.pos]
	switch ch {
	case 'i':
		n.kind = 'i'
		d.pos++
		start := d.pos
		for d.pos < len(d.data) && d.data[d.pos] != 'e' {
			d.pos++
		}
		if d.pos == len(d.data) {
			return nil, ErrInvalid
		}
		s := string(d.data[start:d.pos])
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || strconv.FormatInt(v, 10) != s {
			return nil, ErrInvalid
		}
		n.number = v
		d.pos++
	case 'l', 'd':
		n.kind = ch
		d.pos++
		if ch == 'd' {
			n.dict = map[string]*node{}
		}
		var last []byte
		for d.pos < len(d.data) && d.data[d.pos] != 'e' {
			if ch == 'l' {
				v, err := d.value(depth + 1)
				if err != nil {
					return nil, err
				}
				n.list = append(n.list, v)
			} else {
				key, err := d.stringValue()
				if err != nil {
					return nil, err
				}
				if last != nil && bytes.Compare(last, key) >= 0 {
					return nil, ErrInvalid
				}
				last = key
				v, err := d.value(depth + 1)
				if err != nil {
					return nil, err
				}
				n.keys = append(n.keys, string(key))
				n.dict[string(key)] = v
			}
		}
		if d.pos >= len(d.data) {
			return nil, ErrInvalid
		}
		d.pos++
	default:
		n.kind = 's'
		s, err := d.stringValue()
		if err != nil {
			return nil, err
		}
		n.text = s
	}
	n.end = d.pos
	return n, nil
}
func (d *decoder) stringValue() ([]byte, error) {
	start := d.pos
	for d.pos < len(d.data) && d.data[d.pos] >= '0' && d.data[d.pos] <= '9' {
		d.pos++
	}
	if d.pos == start || d.pos >= len(d.data) || d.data[d.pos] != ':' {
		return nil, ErrInvalid
	}
	size, err := strconv.ParseUint(string(d.data[start:d.pos]), 10, 32)
	if err != nil || (d.pos-start > 1 && d.data[start] == '0') {
		return nil, ErrInvalid
	}
	d.pos++
	if size > uint64(len(d.data)-d.pos) {
		return nil, ErrInvalid
	}
	s := d.data[d.pos : d.pos+int(size)]
	d.pos += int(size)
	return s, nil
}
func field(n *node, key string) *node {
	if n == nil || n.kind != 'd' {
		return nil
	}
	return n.dict[key]
}
func integer(n *node) (int64, bool) {
	if n == nil || n.kind != 'i' {
		return 0, false
	}
	return n.number, true
}
func text(n *node) (string, bool) {
	if n == nil || n.kind != 's' {
		return "", false
	}
	return string(n.text), true
}
