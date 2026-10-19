package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The client configs are edited by splicing bytes: only the LiteZap entry (and, when it
// must be created, the mcpServers object) changes. Key order, formatting, duplicate keys
// and everything else in the file stay byte for byte, which matters for ~/.claude.json,
// a large file Claude Code owns.

type jsonMember struct {
	key                        string
	keyStart, valStart, valEnd int
}

type jsonObject struct {
	open, close int // offsets of '{' and '}'
	members     []jsonMember
}

// parseObject maps the members of the JSON object in b (b holds exactly one object,
// possibly surrounded by whitespace).
func parseObject(b []byte) (jsonObject, error) {
	var o jsonObject
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return o, errors.New("not a JSON object")
	}
	o.open = int(dec.InputOffset()) - 1
	for dec.More() {
		prevEnd := int(dec.InputOffset())
		t, err := dec.Token()
		if err != nil {
			return o, err
		}
		key, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return o, err
		}
		valEnd := int(dec.InputOffset())
		valStart := valEnd - len(raw)
		ks := bytes.IndexByte(b[prevEnd:], '"')
		if ks < 0 || valStart < prevEnd || !bytes.Equal(b[valStart:valEnd], raw) {
			return o, errors.New("unexpected JSON layout")
		}
		o.members = append(o.members, jsonMember{key: key, keyStart: prevEnd + ks, valStart: valStart, valEnd: valEnd})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return o, errors.New("unterminated JSON object")
	}
	o.close = int(dec.InputOffset()) - 1
	return o, nil
}

// find returns the member that counts (encoding/json keeps the last duplicate).
func (o jsonObject) find(key string) (jsonMember, int, bool) {
	for i := len(o.members) - 1; i >= 0; i-- {
		if o.members[i].key == key {
			return o.members[i], i, true
		}
	}
	return jsonMember{}, -1, false
}

// indent is the whitespace before the first key ("\n  " in an indented file, "" in a
// compact one); ok is false for an empty object.
func (o jsonObject) indent(b []byte) (string, bool) {
	if len(o.members) == 0 {
		return "", false
	}
	return string(b[o.open+1 : o.members[0].keyStart]), true
}

func splice(b []byte, start, end int, with []byte) []byte {
	out := make([]byte, 0, len(b)-(end-start)+len(with))
	out = append(out, b[:start]...)
	out = append(out, with...)
	return append(out, b[end:]...)
}

func keyColon(key, indent string) []byte {
	k, _ := json.Marshal(key)
	if strings.Contains(indent, "\n") {
		return append(k, ':', ' ')
	}
	return append(k, ':')
}

// setMember sets key to val in the object b (replacing the effective member, or adding
// it at the end) and returns the new object. childIndent and closeIndent shape an object
// that was empty.
func setMember(b []byte, key string, val []byte, childIndent, closeIndent string) ([]byte, error) {
	o, err := parseObject(b)
	if err != nil {
		return nil, err
	}
	if m, _, ok := o.find(key); ok {
		return splice(b, m.valStart, m.valEnd, val), nil
	}
	if ind, ok := o.indent(b); ok {
		last := o.members[len(o.members)-1]
		add := append([]byte(","+ind), keyColon(key, ind)...)
		return splice(b, last.valEnd, last.valEnd, append(add, val...)), nil
	}
	add := append([]byte(childIndent), keyColon(key, childIndent)...)
	add = append(append(add, val...), closeIndent...)
	return splice(b, o.open+1, o.close, add), nil
}

// deleteMember removes every member named key from the object b.
func deleteMember(b []byte, key string) ([]byte, bool, error) {
	changed := false
	for {
		o, err := parseObject(b)
		if err != nil {
			return nil, false, err
		}
		_, i, ok := o.find(key)
		if !ok {
			return b, changed, nil
		}
		changed = true
		ms := o.members
		switch {
		case i > 0: // ",<ws>"key": value" after the previous member
			b = splice(b, ms[i-1].valEnd, ms[i].valEnd, nil)
		case len(ms) > 1: // first member: up to the next key
			b = splice(b, ms[0].keyStart, ms[1].keyStart, nil)
		default:
			b = splice(b, o.open+1, o.close, nil)
		}
	}
}

func marshalIndent(v any, prefix, unit string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, unit)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// spliceServer sets (val != nil) or removes (val == nil) mcpServers[name] in the config
// file content b, leaving every other byte as it was. changed is false when there was
// nothing to remove.
func spliceServer(b []byte, name string, entry any, remove bool) (out []byte, changed bool, err error) {
	if len(bytes.TrimSpace(b)) == 0 {
		if remove {
			return b, false, nil
		}
		out, err := marshalIndent(map[string]any{"mcpServers": map[string]any{name: entry}}, "", "  ")
		return append(out, '\n'), true, err
	}
	root, err := parseObject(b)
	if err != nil {
		return nil, false, err
	}
	rootInd, ok := root.indent(b)
	if !ok {
		rootInd = "\n  "
	}
	multiline := strings.Contains(rootInd, "\n")
	unit := strings.TrimLeft(rootInd, "\r\n")
	rootClose := "\n"
	if !multiline {
		rootClose = ""
	}

	srv, _, hasSrv := root.find("mcpServers")
	var servers []byte
	if hasSrv && !bytes.Equal(b[srv.valStart:srv.valEnd], []byte("null")) {
		servers = b[srv.valStart:srv.valEnd]
	}
	if remove {
		if servers == nil {
			return b, false, nil
		}
		ns, changed, err := deleteMember(servers, name)
		if err != nil || !changed {
			return b, false, err
		}
		return splice(b, srv.valStart, srv.valEnd, ns), true, nil
	}

	if servers == nil {
		servers = []byte("{}")
	}
	so, err := parseObject(servers)
	if err != nil {
		return nil, false, err
	}
	childInd, ok := so.indent(servers)
	if !ok {
		childInd = rootInd + unit
	}
	var val []byte
	if multiline {
		val, err = marshalIndent(entry, strings.TrimLeft(childInd, "\r\n"), unit)
	} else {
		val, err = marshalIndent(entry, "", "")
	}
	if err != nil {
		return nil, false, err
	}
	ns, err := setMember(servers, name, val, childInd, rootInd)
	if err != nil {
		return nil, false, err
	}
	if hasSrv {
		out = splice(b, srv.valStart, srv.valEnd, ns)
	} else {
		out, err = setMember(b, "mcpServers", ns, rootInd, rootClose)
		if err != nil {
			return nil, false, err
		}
	}
	if !json.Valid(out) {
		return nil, false, fmt.Errorf("internal error: edited JSON is invalid")
	}
	return out, true, nil
}
