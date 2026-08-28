package tools

import (
	"fmt"
)

type reportLayoutParams struct {
	Action    string `json:"action"`
	CustomCSS string `json:"custom_css"`
	BodyClass string `json:"body_class"`
}

type reportLayoutResult struct {
	Action       string
	HasCustomCSS bool
	BodyClass    string
	UISummary    string
}

const maxCustomCSSSize = 10240

func applyReportLayoutMutation(state *ReportState, params reportLayoutParams) (reportLayoutResult, error) {
	if state == nil {
		return reportLayoutResult{}, fmt.Errorf("report state is not initialized")
	}

	if err := validateExactReportField("action", params.Action, true); err != nil {
		return reportLayoutResult{}, err
	}

	switch params.Action {
	case "reset":
		if params.CustomCSS != "" || params.BodyClass != "" {
			return reportLayoutResult{}, fmt.Errorf("reset does not accept custom_css or body_class")
		}
		state.Layout = ReportLayout{}
		state.NeedsFinalize = true
		state.MutationVersion++
		return reportLayoutResult{
			Action:    params.Action,
			UISummary: "报告布局已重置，当前仍为草稿。",
		}, nil
	case "merge":
		if params.CustomCSS == "" && params.BodyClass == "" {
			return reportLayoutResult{}, fmt.Errorf("merge requires custom_css or body_class")
		}
		if params.CustomCSS != "" {
			if len(params.CustomCSS) > maxCustomCSSSize {
				return reportLayoutResult{}, fmt.Errorf("custom_css exceeds maximum allowed size (%d bytes)", maxCustomCSSSize)
			}
			state.Layout.CustomCSS = params.CustomCSS
		}
		if params.BodyClass != "" {
			if sanitizeBodyClass(params.BodyClass) != params.BodyClass {
				return reportLayoutResult{}, fmt.Errorf("body_class must be a space-separated list of CSS class identifiers")
			}
			state.Layout.BodyClass = params.BodyClass
		}
		state.NeedsFinalize = true
		state.MutationVersion++
		return reportLayoutResult{
			Action:       params.Action,
			HasCustomCSS: state.Layout.CustomCSS != "",
			BodyClass:    state.Layout.BodyClass,
			UISummary:    "报告布局已更新，当前仍为草稿。",
		}, nil
	default:
		return reportLayoutResult{}, fmt.Errorf("unknown action: %s", params.Action)
	}
}
