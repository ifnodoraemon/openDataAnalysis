package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type geminiRequest struct {
	SystemInstruction *geminiContent `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}

type geminiFunctionCall struct {
	Name             string                 `json:"name"`
	Args             map[string]interface{} `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

func (l *LLMClient) chatGoogle(ctx context.Context, bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*LLMResponse, error) {
	req := geminiRequest{
		Contents: []geminiContent{},
	}

	if bundle.Policy != "" || bundle.PolicyAppendix != "" {
		systemText := bundle.Policy
		if bundle.PolicyAppendix != "" {
			systemText += "\n\n" + bundle.PolicyAppendix
		}
		req.SystemInstruction = &geminiContent{
			Role: "user",
			Parts: []geminiPart{{Text: systemText}},
		}
	}

	if bundle.Task != "" {
		req.Contents = append(req.Contents, geminiContent{
			Role: "user",
			Parts: []geminiPart{{Text: bundle.Task}},
		})
	}

	for _, c := range bundle.RuntimeContext {
		req.Contents = append(req.Contents, geminiContent{
			Role: "user",
			Parts: []geminiPart{{Text: fmt.Sprintf("[%s]: %s", c.Name, c.Content)}},
		})
	}

	lastMessageWasToolText := false
	for _, h := range bundle.History {
		if h.Role == LLMRoleSystem || h.Role == LLMRoleUser {
			req.Contents = append(req.Contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{Text: h.Content}},
			})
			lastMessageWasToolText = false
		} else if h.Role == LLMRoleAssistant {
			part := geminiPart{}
			if len(h.ToolCalls) > 0 {
				tc := h.ToolCalls[0]
				var args map[string]interface{}
				json.Unmarshal([]byte(tc.Function.Arguments), &args)

				if tc.Function.ThoughtSignature == "" {
					part.Text = fmt.Sprintf("I called tool '%s' with arguments: %s", tc.Function.Name, tc.Function.Arguments)
					lastMessageWasToolText = true
				} else {
					part.FunctionCall = &geminiFunctionCall{
						Name: tc.Function.Name,
						Args: args,
					}
					part.ThoughtSignature = tc.Function.ThoughtSignature
					lastMessageWasToolText = false
				}
			} else {
				part.Text = h.Content
				lastMessageWasToolText = false
			}
			req.Contents = append(req.Contents, geminiContent{
				Role: "model",
				Parts: []geminiPart{part},
			})
		} else if h.Role == LLMRoleTool {
			if lastMessageWasToolText {
				req.Contents = append(req.Contents, geminiContent{
					Role: "user",
					Parts: []geminiPart{{Text: fmt.Sprintf("Tool result: %s", h.Content)}},
				})
			} else {
				var resp map[string]interface{}
				err := json.Unmarshal([]byte(h.Content), &resp)
				if err != nil {
					resp = map[string]interface{}{"result": h.Content}
				}
				
				toolName := "unknown_tool"
				if len(bundle.History) > 0 {
					toolName = "tool_call"
				}

				req.Contents = append(req.Contents, geminiContent{
					Role: "function",
					Parts: []geminiPart{{
						FunctionResponse: &geminiFunctionResponse{
							Name: toolName,
							Response: resp,
						},
					}},
				})
			}
			lastMessageWasToolText = false
		}
	}

	if len(toolSpecs) > 0 {
		tool := geminiTool{
			FunctionDeclarations: []geminiFunctionDeclaration{},
		}
		for _, ts := range toolSpecs {
			tool.FunctionDeclarations = append(tool.FunctionDeclarations, geminiFunctionDeclaration{
				Name: ts.Function.Name,
				Description: ts.Function.Description,
				Parameters: StripAdditionalProperties(ts.Function.Parameters),
			})
		}
		req.Tools = []geminiTool{tool}
	}

	reqBytes, _ := json.Marshal(req)
	log.Printf("[DEBUG] Gemini Request Tools: %s", string(reqBytes))
	
	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", l.model, config.Cfg.LLMAPIKey)
	
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBytes))
	httpReq.Header.Set("Content-Type", "application/json")
	
	resp, err := l.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google API error: %d %s", resp.StatusCode, string(respBytes))
	}
	
	var gResp geminiResponse
	if err := json.Unmarshal(respBytes, &gResp); err != nil {
		return nil, err
	}
	
	if len(gResp.Candidates) == 0 {
		return nil, fmt.Errorf("Google API returned no candidates")
	}
	
	candidate := gResp.Candidates[0]
	llmResp := &LLMResponse{
		Choices: []LLMChoice{{Index: 0}},
	}
	
	msg := LLMMessage{Role: LLMRoleAssistant}
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			msg.Content += part.Text
		}
		if part.FunctionCall != nil {
			argsBytes, _ := json.Marshal(part.FunctionCall.Args)
			msg.ToolCalls = append(msg.ToolCalls, LLMToolCall{
				ID: "call_google",
				Type: "function",
				Function: LLMFunctionCall{
					Name: part.FunctionCall.Name,
					Arguments: string(argsBytes),
					ThoughtSignature: part.ThoughtSignature,
				},
			})
		}
	}
	llmResp.Choices[0].Message = msg
	if len(msg.ToolCalls) > 0 {
		llmResp.Choices[0].FinishReason = "tool_calls"
	} else {
		llmResp.Choices[0].FinishReason = "stop"
	}
	
	return llmResp, nil
}
