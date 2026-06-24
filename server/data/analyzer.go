package data

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SemanticProfile 表示由 LLM 分析给出的一张表的精炼数据语义档案
type SemanticProfile struct {
	TableSummary string             `json:"table_summary"`
	Columns      []SemanticColumn   `json:"columns"`
	Relations    []SemanticRelation `json:"relations"`
}

// SemanticColumn 列语义档案
type SemanticColumn struct {
	Name             string   `json:"name"`
	BusinessAlias    string   `json:"business_alias"`
	Role             string   `json:"role"`              // e.g., "time", "metric", "dimension", "id"
	CalculationLogic string   `json:"calculation_logic"` // 预测口径或逻辑
	Warnings         []string `json:"warnings"`          // 如：脏数据、意义不明的警告
}

// SemanticRelation 表间关联预测
type SemanticRelation struct {
	TargetTable  string `json:"target_table"`
	TargetColumn string `json:"target_column"`
	SourceColumn string `json:"source_column"`
	Reason       string `json:"reason"`
	Verified     bool   `json:"verified"` // 程序化校验是否通过
}

// LLMChatFunc 通用的 LLM 文本对话函数，由调用方提供，避免直接耦合 OpenAI 包
type LLMChatFunc func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// AnalyzeTableSemantics 利用大模型分析表结构的小样本并生成语义化档案
// 签名改为接收 LLMChatFunc 而非直接依赖 openai.Client
var AnalyzeTableSemantics = func(ctx context.Context, chatFn LLMChatFunc, schema *SchemaInfo, activeTables []string) (*SemanticProfile, error) {
	// 构造待分析内容概要
	schemaBytes, _ := json.MarshalIndent(schema, "", "  ")

	// 构造环境里其它表的简易上下文以找出关联机会
	tablesCtx := "No other tables in current session."
	if len(activeTables) > 0 {
		tablesCtx = fmt.Sprintf("Current session has these other tables available for Join analysis: %s", strings.Join(activeTables, ", "))
	}

	prompt := fmt.Sprintf(`You are a senior data analyst. Perform a domain-neutral data semantic pre-analysis on the following newly extracted table Schema.
The target table structure and data sample distribution are as follows:
%s

%s

Analyze the table and output JSON only. The structure must follow this format:
{
  "table_summary": "One-sentence summary of what this table appears to represent",
  "columns": [
    {
      "name": "Original column name",
      "business_alias": "Guessed user-facing or domain alias",
      "role": "time | metric | dimension | id",
      "calculation_logic": "Guess or explanation of the metric's definition; leave empty if none",
      "warnings": ["Low-confidence or anomalous data hints", ...] 
    }
  ],
  "relations": [
    {
      "target_table": "Related table name",
      "target_column": "Guessed related table column",
      "source_column": "This table's column",
      "reason": "Guessed join reason"
    }
  ]
}

Return only raw JSON text. Do not wrap it in Markdown code blocks or add any other text.`, string(schemaBytes), tablesCtx)

	systemPrompt := "You are a specialized data semantic profiler outputting only valid JSON."

	rawJSON, err := chatFn(ctx, systemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM analysis request failed: %w", err)
	}

	rawJSON = strings.TrimSpace(rawJSON)
	// 清理可能误带的 markdown 代码块符号
	rawJSON = strings.TrimPrefix(rawJSON, "```json")
	rawJSON = strings.TrimPrefix(rawJSON, "```")
	rawJSON = strings.TrimSuffix(rawJSON, "```")
	rawJSON = strings.TrimSpace(rawJSON)

	var profile SemanticProfile
	if err := json.Unmarshal([]byte(rawJSON), &profile); err != nil {
		return nil, fmt.Errorf("failed to parse LLM output JSON: %w\nOutput %s", err, rawJSON)
	}

	return &profile, nil
}

// ValidateRelationHints 对 LLM 返回的 relation hints 做程序化校验
// 只保留通过校验的 relations：
// 1. source_column 必须在当前 schema 中存在
// 2. target_table 必须在 activeTables 中存在
// 3. 如果提供了 targetSchemas，target_column 必须在目标表 schema 中存在
func ValidateRelationHints(
	relations []SemanticRelation,
	schema *SchemaInfo,
	activeTables []string,
	targetSchemas map[string]*SchemaInfo,
) []SemanticRelation {
	if len(relations) == 0 {
		return nil
	}

	// 构建查找集
	sourceColumns := make(map[string]struct{}, len(schema.Columns))
	for _, col := range schema.Columns {
		sourceColumns[strings.ToLower(col.Name)] = struct{}{}
	}

	activeTableSet := make(map[string]struct{}, len(activeTables))
	for _, t := range activeTables {
		activeTableSet[strings.ToLower(t)] = struct{}{}
	}

	verified := make([]SemanticRelation, 0, len(relations))
	for _, rel := range relations {
		// 检查 source_column 是否存在
		if _, ok := sourceColumns[strings.ToLower(rel.SourceColumn)]; !ok {
			continue
		}

		// 检查 target_table 是否在 activeTables 中
		if _, ok := activeTableSet[strings.ToLower(rel.TargetTable)]; !ok {
			continue
		}

		// 如果有目标表的 schema，检查 target_column 是否存在
		if targetSchemas != nil {
			if targetSchema, ok := targetSchemas[strings.ToLower(rel.TargetTable)]; ok {
				found := false
				for _, col := range targetSchema.Columns {
					if strings.EqualFold(col.Name, rel.TargetColumn) {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
		}

		rel.Verified = true
		verified = append(verified, rel)
	}

	return verified
}

var MetricQualifierTokens = map[string]struct{}{
	"actual": {}, "adjusted": {}, "booked": {}, "confirmed": {},
	"estimated": {}, "est": {}, "final": {}, "forecast": {},
	"gross": {}, "net": {}, "planned": {}, "plan": {},
	"projected": {}, "raw": {}, "recognized": {}, "target": {},
	"tentative": {}, "unconfirmed": {},
}

func TokenizeColumnName(name string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(name)), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

func InferAmbiguousMetricGroups(columns []ColumnInfo) map[string][]string {
	grouped := make(map[string][]string)
	for _, column := range columns {
		typ := strings.ToUpper(strings.TrimSpace(column.Type))
		if typ != "NUMERIC" && typ != "INTEGER" && typ != "REAL" {
			continue
		}
		tokens := TokenizeColumnName(column.Name)
		if len(tokens) < 2 {
			continue
		}
		core := make([]string, 0, len(tokens))
		qualifierCount := 0
		for _, token := range tokens {
			if _, ok := MetricQualifierTokens[token]; ok {
				qualifierCount++
				continue
			}
			core = append(core, token)
		}
		if qualifierCount == 0 || len(core) == 0 {
			continue
		}
		key := strings.Join(core, "_")
		grouped[key] = append(grouped[key], column.Name)
	}
	result := make(map[string][]string)
	for key, names := range grouped {
		if len(names) >= 2 {
			result[key] = names
		}
	}
	return result
}
