package axiom

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// lookupAny returns the first non-empty env var from the provided names.
// Makes it easy to accept multiple credential env var spellings.
func lookupAny(names ...string) (string, bool) {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v, true
		}
	}
	return "", false
}

// ─── Loose-typed conversions for any-typed row values ────────────────────────
//
// Query backends return column values as []any, numbers come through as float64 or
// json.Number depending on the SDK path, timestamps as RFC3339 strings, and
// all strings are strings. These helpers coerce without panicking.

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	}
	return fmt.Sprint(v)
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func asInt(v any) int64 {
	return int64(asFloat(v))
}

func asTime(v any) time.Time {
	switch t := v.(type) {
	case nil:
		return time.Time{}
	case time.Time:
		return t
	case string:
		if tt, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return tt
		}
		if tt, err := time.Parse(time.RFC3339, t); err == nil {
			return tt
		}
	}
	return time.Time{}
}
