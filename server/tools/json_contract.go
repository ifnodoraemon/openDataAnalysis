package tools

import (
	"encoding/json"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

func decodeToolArgs(args json.RawMessage, out interface{}) error {
	return jsoncontract.Decode(args, out)
}

func ValidateNoArgs(args json.RawMessage) error {
	var params struct{}
	return decodeToolArgs(args, &params)
}
