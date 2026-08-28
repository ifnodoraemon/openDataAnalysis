package agent

import (
	"encoding/json"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

func StripAdditionalProperties(input []byte) ([]byte, error) {
	var obj interface{}
	if err := jsoncontract.Decode(input, &obj); err != nil {
		return nil, fmt.Errorf("invalid tool schema JSON: %w", err)
	}
	cleaned := cleanMap(obj)
	b, err := json.Marshal(cleaned)
	if err != nil {
		return nil, fmt.Errorf("encode cleaned tool schema: %w", err)
	}
	return b, nil
}

func cleanMap(val interface{}) interface{} {
	switch v := val.(type) {
	case map[string]interface{}:
		delete(v, "additionalProperties")
		for k, child := range v {
			v[k] = cleanMap(child)
		}
		return v
	case []interface{}:
		for i, child := range v {
			v[i] = cleanMap(child)
		}
		return v
	default:
		return v
	}
}
