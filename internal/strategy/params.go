package strategy

import (
	"fmt"
	"strconv"
	"strings"
)

func stringParam(params map[string]string, key, def string) string {
	if params == nil {
		return def
	}
	if v, ok := params[key]; ok && v != "" {
		return v
	}
	return def
}

func intParam(params map[string]string, key string, def int64) (int64, error) {
	raw := stringParam(params, key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("param %s: %w", key, err)
	}
	return v, nil
}

func floatParam(params map[string]string, key string, def float64) (float64, error) {
	raw := stringParam(params, key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("param %s: %w", key, err)
	}
	return v, nil
}
