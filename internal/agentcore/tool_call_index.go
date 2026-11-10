package agentcore

import (
	"encoding/json"
	"math"
	"reflect"
)

type callIndexKey struct {
	name string
	args string
}

// unsuccessfulCallIndex indexes ordinary JSON arguments while retaining a
// linear fallback for arbitrary Go values. Indices preserve result order.
type unsuccessfulCallIndex struct {
	next     int
	byKey    map[callIndexKey][]int
	fallback []int
}

func newUnsuccessfulCallIndex() *unsuccessfulCallIndex {
	return &unsuccessfulCallIndex{byKey: make(map[callIndexKey][]int)}
}

func (idx *unsuccessfulCallIndex) find(name string, args map[string]any, results []ToolResult) (ToolResult, bool) {
	for ; idx.next < len(results); idx.next++ {
		results[idx.next].Args = snapshotCallArgs(results[idx.next].Args)
		r := results[idx.next]
		if r.Error == nil && !(r.Disposition == DispositionAttempted && r.Outcome != OutcomeSuccess) {
			continue
		}
		key, ok := callArgsKey(r.Args)
		if !ok {
			idx.fallback = append(idx.fallback, idx.next)
			continue
		}
		k := callIndexKey{r.ToolName, key}
		idx.byKey[k] = append(idx.byKey[k], idx.next)
	}
	key, ok := callArgsKey(args)
	if !ok {
		return priorUnsuccessfulCall(name, args, results)
	}
	candidates := idx.byKey[callIndexKey{name, key}]
	// Merge the two ordered lists so the first matching result wins.
	i, j := 0, 0
	for i < len(candidates) || j < len(idx.fallback) {
		var n int
		if j == len(idx.fallback) || (i < len(candidates) && candidates[i] < idx.fallback[j]) {
			n = candidates[i]
			i++
		} else {
			n = idx.fallback[j]
			j++
		}
		r := results[n]
		if r.ToolName == name && reflect.DeepEqual(r.Args, args) {
			return r, true
		}
	}
	return ToolResult{}, false
}

func snapshotCallArgs(args map[string]any) map[string]any {
	v, ok := copyJSONValue(args, 0, false, make(map[jsonVisit]bool))
	if !ok {
		return args
	}
	return v.(map[string]any)
}

func callArgsKey(args map[string]any) (string, bool) {
	v, ok := copyJSONValue(args, 0, true, make(map[jsonVisit]bool))
	if !ok {
		return "", false
	}
	b, err := json.Marshal(v)
	return string(b), err == nil
}

// Restrict the fast path to ordinary JSON-decoded values. Cycles and values
// nested more than 100 levels use the reflect fallback.
type jsonVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

func copyJSONValue(v any, depth int, normalizeZero bool, visiting map[jsonVisit]bool) (any, bool) {
	if depth > 100 {
		return nil, false
	}
	switch x := v.(type) {
	case nil, bool, string:
		return x, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, false
		}
		if normalizeZero && x == 0 {
			return float64(0), true
		}
		return x, true
	case map[string]any:
		if x == nil {
			return map[string]any(nil), true
		}
		visit := jsonVisit{reflect.Map, uintptr(reflect.ValueOf(x).UnsafePointer())}
		if visiting[visit] {
			return nil, false
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		m := make(map[string]any, len(x))
		for k, value := range x {
			item, ok := copyJSONValue(value, depth+1, normalizeZero, visiting)
			if !ok {
				return nil, false
			}
			m[k] = item
		}
		return m, true
	case []any:
		if x == nil {
			return []any(nil), true
		}
		visit := jsonVisit{reflect.Slice, uintptr(reflect.ValueOf(x).UnsafePointer())}
		if visiting[visit] {
			return nil, false
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		a := make([]any, len(x))
		for i, value := range x {
			item, ok := copyJSONValue(value, depth+1, normalizeZero, visiting)
			if !ok {
				return nil, false
			}
			a[i] = item
		}
		return a, true
	default:
		return nil, false
	}
}
