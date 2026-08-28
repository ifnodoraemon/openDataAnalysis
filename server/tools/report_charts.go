package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

var chartDimensionRegexp = regexp.MustCompile(`^(?:0|[0-9]+(?:\.[0-9]+)?(?:px|%|em|rem|vh|vw))$`)

type reportChartMutationResult struct {
	ChartID  string
	Title    string
	ChartRef string
	Replaced bool
}

type reportChartScopeError struct {
	ChartID string
}

func (e reportChartScopeError) Error() string {
	return fmt.Sprintf("chart %s is outside editable scope", e.ChartID)
}

type reportChartValidationError struct {
	ChartID string
	Title   string
	Detail  string
}

func (e reportChartValidationError) Error() string {
	if strings.TrimSpace(e.Detail) == "" {
		return "invalid chart spec"
	}
	return e.Detail
}

func applyReportChartMutation(state *ReportState, editState *ReportEditState, params createChartParams) (reportChartMutationResult, error) {
	if state == nil {
		return reportChartMutationResult{}, fmt.Errorf("report state is not initialized")
	}
	if err := validateExactReportField("chart_id", params.ChartID, true); err != nil {
		return reportChartMutationResult{}, reportChartValidationError{ChartID: params.ChartID, Title: params.Title, Detail: err.Error()}
	}
	for index, source := range params.Sources {
		if err := validateEvidenceRefShape(source); err != nil {
			return reportChartMutationResult{}, reportChartValidationError{ChartID: params.ChartID, Title: params.Title, Detail: fmt.Sprintf("sources[%d]: %v", index, err)}
		}
	}
	for field, value := range map[string]string{"width": params.Width, "height": params.Height} {
		if err := validateExactReportField(field, value, false); err != nil {
			return reportChartMutationResult{}, reportChartValidationError{ChartID: params.ChartID, Title: params.Title, Detail: err.Error()}
		}
		if value != "" && !chartDimensionRegexp.MatchString(value) {
			return reportChartMutationResult{}, reportChartValidationError{ChartID: params.ChartID, Title: params.Title, Detail: fmt.Sprintf("%s must be a non-negative CSS length using px, %%, em, rem, vh, or vw", field)}
		}
	}

	option, err := resolveChartOption(params)
	if err != nil {
		return reportChartMutationResult{}, reportChartValidationError{
			ChartID: params.ChartID,
			Title:   params.Title,
			Detail:  err.Error(),
		}
	}
	if editState != nil && !editState.ChartMutationAllowed(params.ChartID) {
		return reportChartMutationResult{}, reportChartScopeError{ChartID: params.ChartID}
	}

	chart := ChartData{
		ID:      params.ChartID,
		Option:  option,
		Width:   params.Width,
		Height:  params.Height,
		Sources: params.Sources,
	}

	replaced := false
	for i := range state.Charts {
		if state.Charts[i].ID == params.ChartID {
			state.Charts[i] = chart
			replaced = true
			break
		}
	}
	if !replaced {
		state.Charts = append(state.Charts, chart)
	}
	state.NeedsFinalize = true
	state.MutationVersion++

	return reportChartMutationResult{
		ChartID:  params.ChartID,
		Title:    params.Title,
		ChartRef: "{{chart:" + params.ChartID + "}}",
		Replaced: replaced,
	}, nil
}

func resolveChartOption(params createChartParams) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(params.Option))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("option is required")
	}
	var option map[string]interface{}
	if err := jsoncontract.Decode(params.Option, &option); err != nil || option == nil {
		return nil, fmt.Errorf("option must be a JSON object")
	}
	return params.Option, nil
}
