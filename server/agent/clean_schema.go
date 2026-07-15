package agent

import "encoding/json"

func StripAdditionalProperties(input []byte) []byte {
	var obj interface{}
	if err := json.Unmarshal(input, &obj); err != nil {
		return input
	}
	cleaned := cleanMap(obj)
	b, _ := json.Marshal(cleaned)
	return b
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
