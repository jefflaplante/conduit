package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

// Document is an order- and formatting-preserving view of a raw config.json
// (conduit-rmho). The live config reload edits this document rather than
// re-marshalling the Config struct, because the struct holds expanded
// ${ENV_VAR} values (secrets) and drops unknown and deprecated keys; writing
// it back would leak secrets to disk and lose keys.
//
// Only the path from the root to an edited value is re-rendered; every
// untouched value is written back byte for byte (including its original
// indentation, number spelling and string escapes), so an edit produces a
// minimal diff. Re-rendered containers use the file's own indent unit.
type Document struct {
	prefix, suffix []byte // whitespace around the root value
	root           *docNode
	indent         string
}

type docKind int

const (
	docScalar docKind = iota
	docObject
	docArray
)

type docNode struct {
	kind docKind
	// raw is the node's original source; nil once the node or any
	// descendant changed (it is then rendered from its parts).
	raw    []byte
	keys   []string // object keys, in source order
	fields map[string]*docNode
	items  []*docNode
	scalar []byte // JSON of a scalar
}

// maxDocDepth bounds nesting so a hostile document cannot blow the stack.
const maxDocDepth = 256

// ParseDocument parses a JSON document whose root must be an object.
// Duplicate keys keep their first position and their last value, like
// encoding/json.
func ParseDocument(data []byte) (*Document, error) {
	p := &docParser{data: data}
	p.ws()
	start := p.pos
	root, err := p.value(0)
	if err != nil {
		return nil, err
	}
	end := p.pos
	p.ws()
	if p.pos != len(data) {
		return nil, fmt.Errorf("config document: unexpected data at offset %d", p.pos)
	}
	if root.kind != docObject {
		return nil, errors.New("config document: root must be a JSON object")
	}
	return &Document{
		prefix: append([]byte(nil), data[:start]...),
		suffix: append([]byte(nil), data[end:]...),
		root:   root,
		indent: detectIndent(data),
	}, nil
}

// detectIndent returns the whitespace that starts the first indented line,
// taken as the file's indent unit; "  " when there is none.
func detectIndent(data []byte) string {
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		j := i + 1
		for j < len(data) && (data[j] == ' ' || data[j] == '\t') {
			j++
		}
		if j > i+1 && j < len(data) && data[j] != '\n' && data[j] != '\r' {
			return string(data[i+1 : j])
		}
	}
	return "  "
}

type docParser struct {
	data []byte
	pos  int
}

func (p *docParser) ws() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *docParser) errf(format string, args ...interface{}) error {
	return fmt.Errorf("config document: offset %d: %s", p.pos, fmt.Sprintf(format, args...))
}

func (p *docParser) value(depth int) (*docNode, error) {
	if depth > maxDocDepth {
		return nil, p.errf("nesting too deep")
	}
	p.ws()
	if p.pos >= len(p.data) {
		return nil, p.errf("unexpected end of input")
	}
	start := p.pos
	var n *docNode
	switch p.data[p.pos] {
	case '{':
		n = &docNode{kind: docObject, fields: map[string]*docNode{}}
		p.pos++
		p.ws()
		if p.pos < len(p.data) && p.data[p.pos] == '}' {
			p.pos++
			break
		}
		for {
			p.ws()
			if p.pos >= len(p.data) || p.data[p.pos] != '"' {
				return nil, p.errf("expected object key")
			}
			ks := p.pos
			if err := p.skipString(); err != nil {
				return nil, err
			}
			var key string
			if err := json.Unmarshal(p.data[ks:p.pos], &key); err != nil {
				return nil, p.errf("bad object key: %v", err)
			}
			p.ws()
			if p.pos >= len(p.data) || p.data[p.pos] != ':' {
				return nil, p.errf("expected ':' after key %q", key)
			}
			p.pos++
			child, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			if _, dup := n.fields[key]; !dup {
				n.keys = append(n.keys, key)
			}
			n.fields[key] = child
			p.ws()
			if p.pos >= len(p.data) {
				return nil, p.errf("unterminated object")
			}
			if p.data[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.data[p.pos] == '}' {
				p.pos++
				break
			}
			return nil, p.errf("expected ',' or '}' in object")
		}
	case '[':
		n = &docNode{kind: docArray}
		p.pos++
		p.ws()
		if p.pos < len(p.data) && p.data[p.pos] == ']' {
			p.pos++
			break
		}
		for {
			child, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, child)
			p.ws()
			if p.pos >= len(p.data) {
				return nil, p.errf("unterminated array")
			}
			if p.data[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.data[p.pos] == ']' {
				p.pos++
				break
			}
			return nil, p.errf("expected ',' or ']' in array")
		}
	case '"':
		if err := p.skipString(); err != nil {
			return nil, err
		}
		n = &docNode{kind: docScalar}
	default:
		for p.pos < len(p.data) {
			c := p.data[p.pos]
			if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				break
			}
			p.pos++
		}
		n = &docNode{kind: docScalar}
	}
	n.raw = p.data[start:p.pos]
	if n.kind == docScalar {
		if !json.Valid(n.raw) {
			return nil, fmt.Errorf("config document: offset %d: invalid value %q", start, string(n.raw))
		}
		n.scalar = n.raw
	}
	return n, nil
}

// skipString advances past a JSON string starting at p.pos (a '"').
func (p *docParser) skipString() error {
	p.pos++
	for p.pos < len(p.data) {
		switch c := p.data[p.pos]; {
		case c == '\\':
			p.pos += 2
		case c == '"':
			p.pos++
			return nil
		case c < 0x20:
			return p.errf("control character in string")
		default:
			p.pos++
		}
	}
	return p.errf("unterminated string")
}

// Bytes renders the document.
func (d *Document) Bytes() []byte {
	var b bytes.Buffer
	b.Write(d.prefix)
	d.render(&b, d.root, 0)
	b.Write(d.suffix)
	return b.Bytes()
}

func (d *Document) render(b *bytes.Buffer, n *docNode, depth int) {
	if n.raw != nil {
		b.Write(n.raw)
		return
	}
	pad := func(k int) {
		for i := 0; i < k; i++ {
			b.WriteString(d.indent)
		}
	}
	switch n.kind {
	case docScalar:
		b.Write(n.scalar)
	case docObject:
		if len(n.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range n.keys {
			pad(depth + 1)
			b.Write(encodeJSON(k))
			b.WriteString(": ")
			d.render(b, n.fields[k], depth+1)
			if i < len(n.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		pad(depth)
		b.WriteByte('}')
	case docArray:
		if len(n.items) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, it := range n.items {
			pad(depth + 1)
			d.render(b, it, depth+1)
			if i < len(n.items)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		pad(depth)
		b.WriteByte(']')
	}
}

// encodeJSON marshals v without HTML escaping. v is always a JSON-shaped
// value here, so Encode cannot fail.
func encodeJSON(v interface{}) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// nodeBytes renders one node on its own (depth 0).
func (d *Document) nodeBytes(n *docNode) []byte {
	var b bytes.Buffer
	d.render(&b, n, 0)
	return b.Bytes()
}

// newDocNode builds a detached node for v, laid out like a re-rendered
// container (not compact).
func newDocNode(v interface{}) (*docNode, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("value is not JSON-encodable: %w", err)
	}
	p := &docParser{data: encodeJSONRaw(data)}
	n, err := p.value(0)
	if err != nil {
		return nil, err
	}
	clearContainerRaw(n)
	return n, nil
}

// encodeJSONRaw re-encodes compact JSON without HTML escapes (json.Marshal
// escapes <, > and &).
func encodeJSONRaw(data []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return data
	}
	return encodeJSON(v)
}

func clearContainerRaw(n *docNode) {
	if n.kind == docScalar {
		return
	}
	n.raw = nil
	for _, c := range n.fields {
		clearContainerRaw(c)
	}
	for _, c := range n.items {
		clearContainerRaw(c)
	}
}

// itemName returns the "name" string of an array element that is an
// object, or "".
func itemName(n *docNode) string {
	if n.kind != docObject {
		return ""
	}
	f := n.fields["name"]
	if f == nil || f.kind != docScalar {
		return ""
	}
	var s string
	if json.Unmarshal(f.scalar, &s) != nil {
		return ""
	}
	return s
}

// findItem resolves an array segment: the element whose "name" is seg,
// else a numeric index.
func findItem(arr *docNode, seg string) (int, bool) {
	for i, it := range arr.items {
		if itemName(it) == seg {
			return i, true
		}
	}
	if i, err := strconv.Atoi(seg); err == nil && i >= 0 && i < len(arr.items) {
		return i, true
	}
	return -1, false
}

// canonicalSeg names an array element in a resolved path: its "name" when
// it has one, else its index.
func canonicalSeg(arr *docNode, i int) string {
	if name := itemName(arr.items[i]); name != "" {
		return name
	}
	return strconv.Itoa(i)
}

// Lookup resolves path and returns the value there as generic JSON, the
// canonical path (array elements named by their "name" field) and whether
// the value exists. A missing object key is not an error (found=false,
// the rest of the path is kept as given); a missing array element, or
// descending into a scalar, is.
func (d *Document) Lookup(path []string) (value interface{}, canonical []string, found bool, err error) {
	n, canonical, err := d.walk(path)
	if err != nil || n == nil {
		return nil, canonical, false, err
	}
	if err := json.Unmarshal(d.nodeBytes(n), &value); err != nil {
		return nil, canonical, false, err
	}
	return value, canonical, true, nil
}

func (d *Document) walk(path []string) (*docNode, []string, error) {
	if len(path) == 0 {
		return nil, nil, errors.New("empty config path")
	}
	cur := d.root
	canonical := make([]string, 0, len(path))
	for i, seg := range path {
		switch cur.kind {
		case docObject:
			next := cur.fields[seg]
			canonical = append(canonical, seg)
			if next == nil {
				return nil, append(canonical, path[i+1:]...), nil
			}
			cur = next
		case docArray:
			idx, ok := findItem(cur, seg)
			if !ok {
				return nil, append(canonical, path[i:]...), fmt.Errorf("%s: no element named %q", joinPath(canonical), seg)
			}
			canonical = append(canonical, canonicalSeg(cur, idx))
			cur = cur.items[idx]
		default:
			return nil, append(canonical, path[i:]...), fmt.Errorf("%s is not an object or array", joinPath(canonical))
		}
	}
	return cur, canonical, nil
}

// Set assigns value at path (del removes the key or array element
// instead). Missing intermediate objects are created. It returns the
// canonical path and whether the document changed; setting a value equal
// to the current one (as JSON) is not a change.
func (d *Document) Set(path []string, value interface{}, del bool) (canonical []string, changed bool, err error) {
	old, canonical, found, err := d.Lookup(path)
	if err != nil {
		return canonical, false, err
	}
	var nn *docNode
	if del {
		if !found {
			return canonical, false, nil
		}
	} else {
		nn, err = newDocNode(value)
		if err != nil {
			return canonical, false, err
		}
		if found {
			var nv interface{}
			if err := json.Unmarshal(d.nodeBytes(nn), &nv); err != nil {
				return canonical, false, err
			}
			if reflect.DeepEqual(old, nv) {
				return canonical, false, nil
			}
		}
	}

	cur := d.root
	cur.raw = nil
	last := len(path) - 1
	for i := 0; i < last; i++ {
		seg := path[i]
		if cur.kind == docArray {
			idx, _ := findItem(cur, seg)
			cur = cur.items[idx]
		} else {
			next := cur.fields[seg]
			if next == nil {
				next = &docNode{kind: docObject, fields: map[string]*docNode{}}
				cur.keys = append(cur.keys, seg)
				cur.fields[seg] = next
			}
			cur = next
		}
		cur.raw = nil
	}
	seg := path[last]
	if cur.kind == docArray {
		idx, _ := findItem(cur, seg)
		if del {
			cur.items = append(cur.items[:idx], cur.items[idx+1:]...)
		} else {
			cur.items[idx] = nn
		}
		return canonical, true, nil
	}
	if cur.kind != docObject {
		return canonical, false, fmt.Errorf("%s is not an object", joinPath(canonical[:last]))
	}
	if del {
		delete(cur.fields, seg)
		for i, k := range cur.keys {
			if k == seg {
				cur.keys = append(cur.keys[:i], cur.keys[i+1:]...)
				break
			}
		}
		return canonical, true, nil
	}
	if _, ok := cur.fields[seg]; !ok {
		cur.keys = append(cur.keys, seg)
	}
	cur.fields[seg] = nn
	return canonical, true, nil
}

// joinPath renders a path for messages and audit logs.
func joinPath(path []string) string {
	var b bytes.Buffer
	for i, s := range path {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s)
	}
	return b.String()
}

// JoinPath renders a config key path ("ai.providers.z-ai.timeout_seconds").
func JoinPath(path []string) string { return joinPath(path) }
