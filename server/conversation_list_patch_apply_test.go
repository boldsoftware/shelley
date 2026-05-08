package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// applyTestPatch is a minimal RFC 6902 applier used by tests to verify the
// patches the server emits. Production clients use this same algorithm in JS.
func applyTestPatch(state []ConversationWithState, ops []conversationListPatchOp) ([]ConversationWithState, error) {
	if state == nil {
		state = []ConversationWithState{}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	for _, op := range ops {
		doc, err = applyOp(doc, op)
		if err != nil {
			return nil, err
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var next []ConversationWithState
	if err := json.Unmarshal(out, &next); err != nil {
		return nil, err
	}
	return next, nil
}

func applyOp(doc any, op conversationListPatchOp) (any, error) {
	switch op.Op {
	case "replace":
		if op.Path == "" {
			var v any
			if err := json.Unmarshal(op.Value, &v); err != nil {
				return nil, err
			}
			return v, nil
		}
		return setAt(doc, op.Path, op.Value, true)
	case "add":
		return setAt(doc, op.Path, op.Value, false)
	case "remove":
		return removeAt(doc, op.Path)
	case "move":
		val, err := getAt(doc, op.From)
		if err != nil {
			return nil, err
		}
		doc, err = removeAt(doc, op.From)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return nil, err
		}
		return setAt(doc, op.Path, raw, false)
	}
	return nil, fmt.Errorf("unsupported op %q", op.Op)
}

func splitPointer(p string) []string {
	if p == "" {
		return nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil
	}
	parts := strings.Split(p[1:], "/")
	for i, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		parts[i] = part
	}
	return parts
}

func walk(doc any, parts []string) (any, error) {
	for _, part := range parts {
		switch v := doc.(type) {
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("bad index %q", part)
			}
			doc = v[idx]
		case map[string]any:
			doc = v[part]
		default:
			return nil, fmt.Errorf("cannot traverse %T at %q", doc, part)
		}
	}
	return doc, nil
}

func getAt(doc any, path string) (any, error) {
	return walk(doc, splitPointer(path))
}

func setAt(doc any, path string, raw json.RawMessage, mustExist bool) (any, error) {
	parts := splitPointer(path)
	if len(parts) == 0 {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return v, nil
	}
	parent, err := walk(doc, parts[:len(parts)-1])
	if err != nil {
		return nil, err
	}
	last := parts[len(parts)-1]
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	switch p := parent.(type) {
	case []any:
		idx, err := strconv.Atoi(last)
		if err != nil || idx < 0 || idx > len(p) {
			return nil, fmt.Errorf("bad array index %q", last)
		}
		if mustExist && idx >= len(p) {
			return nil, fmt.Errorf("index out of range")
		}
		if idx == len(p) {
			p = append(p, v)
		} else if mustExist {
			p[idx] = v
		} else {
			p = append(p[:idx+1], p[idx:]...)
			p[idx] = v
		}
		return replaceAt(doc, parts[:len(parts)-1], p)
	case map[string]any:
		if mustExist {
			if _, ok := p[last]; !ok {
				return nil, fmt.Errorf("missing key %q", last)
			}
		}
		p[last] = v
		return doc, nil
	}
	return nil, fmt.Errorf("cannot set on %T", parent)
}

func removeAt(doc any, path string) (any, error) {
	parts := splitPointer(path)
	if len(parts) == 0 {
		return nil, fmt.Errorf("cannot remove root")
	}
	parent, err := walk(doc, parts[:len(parts)-1])
	if err != nil {
		return nil, err
	}
	last := parts[len(parts)-1]
	switch p := parent.(type) {
	case []any:
		idx, err := strconv.Atoi(last)
		if err != nil || idx < 0 || idx >= len(p) {
			return nil, fmt.Errorf("bad array index %q", last)
		}
		p = append(p[:idx], p[idx+1:]...)
		return replaceAt(doc, parts[:len(parts)-1], p)
	case map[string]any:
		delete(p, last)
		return doc, nil
	}
	return nil, fmt.Errorf("cannot remove from %T", parent)
}

func replaceAt(doc any, parts []string, value any) (any, error) {
	if len(parts) == 0 {
		return value, nil
	}
	parent, err := walk(doc, parts[:len(parts)-1])
	if err != nil {
		return nil, err
	}
	last := parts[len(parts)-1]
	switch p := parent.(type) {
	case []any:
		idx, _ := strconv.Atoi(last)
		p[idx] = value
		return doc, nil
	case map[string]any:
		p[last] = value
		return doc, nil
	}
	return nil, fmt.Errorf("cannot replace in %T", parent)
}
