package transform

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Apply filters JSON data according to include/exclude field lists.
// If include is non-empty, only those fields (and their parents) are kept.
// Fields listed in exclude are always removed.
// Both lists support dot-separated paths for nested fields (e.g. "address.city").
// Non-object JSON (arrays, scalars) is returned unchanged.
func Apply(data []byte, include, exclude []string) ([]byte, error) {
	if len(include) == 0 && len(exclude) == 0 {
		return data, nil
	}

	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parsing response json: %w", err)
	}

	result := filterValue(v, include, exclude, "")
	return json.Marshal(result)
}

func filterValue(v interface{}, include, exclude []string, prefix string) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return filterObject(val, include, exclude, prefix)
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, elem := range val {
			out[i] = filterValue(elem, include, exclude, prefix)
		}
		return out
	default:
		return v
	}
}

func filterObject(m map[string]interface{}, include, exclude []string, prefix string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, val := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}

		if matchesAny(path, exclude) {
			continue
		}

		if len(include) > 0 && !matchesAny(path, include) && !isPrefixOf(path, include) {
			continue
		}

		if nested, ok := val.(map[string]interface{}); ok {
			val = filterObject(nested, include, exclude, path)
		}
		out[k] = val
	}
	return out
}

// matchesAny returns true if path equals or is a child of any pattern.
func matchesAny(path string, patterns []string) bool {
	for _, p := range patterns {
		if p == path || strings.HasPrefix(path, p+".") {
			return true
		}
	}
	return false
}

// isPrefixOf returns true if path is a parent segment of any include pattern.
// This keeps intermediate objects when a deep field is included.
func isPrefixOf(path string, include []string) bool {
	for _, p := range include {
		if strings.HasPrefix(p, path+".") {
			return true
		}
	}
	return false
}
