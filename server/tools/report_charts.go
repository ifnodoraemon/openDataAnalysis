package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
		Width:   "100%",
		Height:  "400px",
		Sources: params.Sources,
	}

	replaced := false
	for i := range state.Charts {
		if strings.TrimSpace(state.Charts[i].ID) == params.ChartID {
			state.Charts[i] = chart
			replaced = true
			break
		}
	}
	if !replaced {
		state.Charts = append(state.Charts, chart)
	}
	state.NeedsFinalize = true

	return reportChartMutationResult{
		ChartID:  params.ChartID,
		Title:    params.Title,
		ChartRef: "{{chart:" + params.ChartID + "}}",
		Replaced: replaced,
	}, nil
}

func resolveChartOption(params createChartParams) (json.RawMessage, error) {
	if hasRawChartOption(params.Option) {
		return params.Option, nil
	}
	return buildOptionFromDSL(params)
}
