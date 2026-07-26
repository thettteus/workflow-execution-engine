package engine

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// evalCondition reports whether cond holds against an upstream artifact's JSON.
// A nil cond is an unconditional edge (always true).
//
// This is the hand-rolled replacement for a JSON-path library (see EXECUTION.md
// §1a): a dotted-path walk over encoding/json plus a scalar comparison. It
// deliberately supports only what a conditional edge needs — no wildcards,
// filters, or query syntax.
func evalCondition(cond *domain.Condition, artifactJSON []byte) (bool, error) {
	if cond == nil {
		return true, nil
	}

	var doc any
	if err := json.Unmarshal(artifactJSON, &doc); err != nil {
		return false, fmt.Errorf("condition on %q: upstream artifact is not valid JSON: %w", cond.Path, err)
	}

	got, found := lookupPath(doc, cond.Path)

	switch cond.Op {
	case domain.OpExists:
		return found, nil
	case domain.OpTruthy:
		return found && truthy(got), nil
	case domain.OpEq, domain.OpNe, domain.OpGt, domain.OpGte, domain.OpLt, domain.OpLte:
		if !found {
			// A missing value can't equal or order against anything; the only
			// true comparison for an absent path is "ne".
			return cond.Op == domain.OpNe, nil
		}
		return compare(got, cond.Op, cond.Value)
	default:
		return false, fmt.Errorf("condition on %q: unknown operator %q", cond.Path, cond.Op)
	}
}

// lookupPath walks a dotted path through nested JSON. Object keys index maps;
// numeric segments index arrays. Returns (value, true) if the whole path
// resolves, else (nil, false). An empty path returns the document itself.
func lookupPath(doc any, path string) (any, bool) {
	if path == "" {
		return doc, true
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// compare applies an ordering/equality operator between the value found at the
// path (got) and the predicate's literal (want).
func compare(got any, op domain.CompareOp, want any) (bool, error) {
	switch op {
	case domain.OpEq:
		return equalScalar(got, want), nil
	case domain.OpNe:
		return !equalScalar(got, want), nil
	case domain.OpGt, domain.OpGte, domain.OpLt, domain.OpLte:
		g, ok1 := toFloat(got)
		w, ok2 := toFloat(want)
		if !ok1 || !ok2 {
			return false, fmt.Errorf("operator %q requires numbers, got %T and %T", op, got, want)
		}
		switch op {
		case domain.OpGt:
			return g > w, nil
		case domain.OpGte:
			return g >= w, nil
		case domain.OpLt:
			return g < w, nil
		default: // OpLte
			return g <= w, nil
		}
	default:
		return false, fmt.Errorf("unknown operator %q", op)
	}
}

// equalScalar compares two JSON scalars. Numbers compare by value regardless of
// int/float representation; everything else compares by type and value.
func equalScalar(a, b any) bool {
	if af, ok := toFloat(a); ok {
		if bf, ok := toFloat(b); ok {
			return af == bf
		}
		return false
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case nil:
		return b == nil
	default:
		return false
	}
}

// toFloat converts JSON/YAML numeric values to float64. It accepts the types
// encoding/json (float64, json.Number) and yaml.v3 (int, int64) produce.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// truthy reports JavaScript-style truthiness for a JSON value.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		if f, ok := toFloat(v); ok {
			return f != 0
		}
		return true
	}
}
