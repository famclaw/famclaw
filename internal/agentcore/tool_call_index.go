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
	v, ok := copyJSONValue(args, 0, false, newCopyBudget(false))
	if !ok {
		return args
	}
	return v.(map[string]any)
}

func callArgsKey(args map[string]any) (string, bool) {
	v, ok := copyJSONValue(args, 0, true, newCopyBudget(true))
	if !ok {
		return "", false
	}
	b, err := json.Marshal(v)
	return string(b), err == nil
}

// Snapshots are best effort: unsupported or cyclic values, deeply nested values,
// and values exceeding the node budget retain their original arguments and use
// the reflect fallback.
// String and key bytes are immutable and are not copied by a snapshot. The key
// encoding byte budget bounds raw keys and strings; JSON escaping can expand
// each byte to at most six bytes, with bounded structural overhead.
const (
	maxCopyNodes       = 16384
	maxCopyStringBytes = 1 << 20
)

type copyBudget struct {
	nodes      int
	bytes      int
	limitBytes bool
	visiting   map[jsonVisit]bool
}

func newCopyBudget(limitBytes bool) *copyBudget {
	return &copyBudget{nodes: maxCopyNodes, bytes: maxCopyStringBytes, limitBytes: limitBytes, visiting: make(map[jsonVisit]bool)}
}

type jsonVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

func copyJSONValue(v any, depth int, normalizeZero bool, budget *copyBudget) (any, bool) {
	if depth > 100 || budget.nodes == 0 {
		return nil, false
	}
	budget.nodes--
	switch x := v.(type) {
	case nil, bool:
		return x, true
	case string:
		if budget.limitBytes && len(x) > budget.bytes {
			return nil, false
		}
		if budget.limitBytes {
			budget.bytes -= len(x)
		}
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
		if budget.visiting[visit] {
			return nil, false
		}
		budget.visiting[visit] = true
		defer delete(budget.visiting, visit)
		if len(x) > budget.nodes {
			return nil, false
		}
		for k := range x {
			if budget.limitBytes && len(k) > budget.bytes {
				return nil, false
			}
			if budget.limitBytes {
				budget.bytes -= len(k)
			}
		}
		m := make(map[string]any, len(x))
		for k, value := range x {
			item, ok := copyJSONValue(value, depth+1, normalizeZero, budget)
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
		if budget.visiting[visit] {
			return nil, false
		}
		budget.visiting[visit] = true
		defer delete(budget.visiting, visit)
		if len(x) > budget.nodes {
			return nil, false
		}
		a := make([]any, len(x))
		for i, value := range x {
			item, ok := copyJSONValue(value, depth+1, normalizeZero, budget)
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
