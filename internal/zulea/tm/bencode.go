// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package tm

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

// value is a decoded bencode value: int64, []byte, []value or *dict.
type value any

// dict preserves key order and the raw span of every value.
type dict struct {
	keys   []string
	values map[string]value
	raw    map[string][]byte
}

const maxDepth = 64

type decoder struct {
	data  []byte
	pos   int
	depth int
}

var errBencode = errors.New("tm: invalid bencode")

func bdecodeErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBencode, fmt.Sprintf(format, args...))
}

// decodeStrict decodes one canonical bencode value spanning all of data.
// Canonical means sorted unique dictionary keys and no leading zeros, so the
// raw info span is exactly what a client hashes.
func decodeStrict(data []byte) (value, error) {
	d := &decoder{data: data}
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	if d.pos != len(d.data) {
		return nil, bdecodeErr("trailing data after root value")
	}
	return v, nil
}

func (d *decoder) value() (value, error) {
	if d.pos >= len(d.data) {
		return nil, bdecodeErr("unexpected end of data")
	}
	switch c := d.data[d.pos]; {
	case c == 'i':
		return d.integer()
	case c == 'l':
		return d.list()
	case c == 'd':
		return d.dictionary()
	case c >= '0' && c <= '9':
		return d.bytes()
	default:
		return nil, bdecodeErr("unexpected byte %q at %d", c, d.pos)
	}
}

func (d *decoder) integer() (value, error) {
	end := bytes.IndexByte(d.data[d.pos:], 'e')
	if end < 0 {
		return nil, bdecodeErr("unterminated integer")
	}
	text := string(d.data[d.pos+1 : d.pos+end])
	if text == "" || text == "-" || text == "-0" ||
		(len(text) > 1 && text[0] == '0') || (len(text) > 2 && text[0] == '-' && text[1] == '0') {
		return nil, bdecodeErr("non-canonical integer %q", text)
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil, bdecodeErr("integer %q: %v", text, err)
	}
	d.pos += end + 1
	return n, nil
}

func (d *decoder) bytes() (value, error) {
	b, err := d.rawBytes()
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (d *decoder) rawBytes() ([]byte, error) {
	colon := bytes.IndexByte(d.data[d.pos:], ':')
	if colon < 0 {
		return nil, bdecodeErr("unterminated string length")
	}
	text := string(d.data[d.pos : d.pos+colon])
	if len(text) > 1 && text[0] == '0' {
		return nil, bdecodeErr("non-canonical string length %q", text)
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 0 {
		return nil, bdecodeErr("string length %q", text)
	}
	start := d.pos + colon + 1
	if n > len(d.data)-start {
		return nil, bdecodeErr("string exceeds data")
	}
	d.pos = start + n
	return d.data[start:d.pos], nil
}

func (d *decoder) list() (value, error) {
	if d.depth++; d.depth > maxDepth {
		return nil, bdecodeErr("nesting too deep")
	}
	defer func() { d.depth-- }()
	d.pos++
	var out []value
	for {
		if d.pos >= len(d.data) {
			return nil, bdecodeErr("unterminated list")
		}
		if d.data[d.pos] == 'e' {
			d.pos++
			return out, nil
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func (d *decoder) dictionary() (value, error) {
	if d.depth++; d.depth > maxDepth {
		return nil, bdecodeErr("nesting too deep")
	}
	defer func() { d.depth-- }()
	d.pos++
	out := &dict{values: map[string]value{}, raw: map[string][]byte{}}
	prev := ""
	for {
		if d.pos >= len(d.data) {
			return nil, bdecodeErr("unterminated dictionary")
		}
		if d.data[d.pos] == 'e' {
			d.pos++
			return out, nil
		}
		if c := d.data[d.pos]; c < '0' || c > '9' {
			return nil, bdecodeErr("dictionary key is not a string")
		}
		k, err := d.rawBytes()
		if err != nil {
			return nil, err
		}
		key := string(k)
		if len(out.keys) > 0 && key <= prev {
			return nil, bdecodeErr("dictionary keys not sorted or duplicated at %q", key)
		}
		start := d.pos
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		out.keys = append(out.keys, key)
		out.values[key] = v
		out.raw[key] = d.data[start:d.pos]
		prev = key
	}
}
