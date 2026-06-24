package tools

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/data"
)

// Tool 工具接口
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(args json.RawMessage) (string, error)
}

type StrictTool interface {
	Strict() bool
}

type FunctionSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      bool
}

type ToolSpec struct {
	Type     string
	Function FunctionSpec
}

// Registry 工具注册表
type Registry struct {
	tools map[string]Tool
}

// SubgoalChecker 提供了一种避免循环依赖的方式，让图表等工具可以访问当前子目标状态
type SubgoalChecker interface {
	CanFinalize() (bool, []string)
}

type QueryLocker interface {
	RLockQuery()
	RUnlockQuery()
}

// ToolContext 提供给工具初始化时的上下文依赖
type ToolContext struct {
	Ingester                   *data.Ingester
	ReportState                *ReportState
	EditState                  *ReportEditState
	Memory                     any            // Type: *agent.WorkingMemory
	Subgoals                   SubgoalChecker // Instead of any, we use an interface to avoid circular imports
	DelegateRegistry           *Registry
	EmitFunc                   func(any) // Type: func(agent.WSEvent)
	SessionID                  string
	WorkspaceID                string
	SessionSourcesProvider     SessionSourcesProvider
	ProfileDetailProvider      ProfileDetailProvider
	GovernanceProvider         GovernanceProvider
	ConfirmedOverridesProvider ConfirmedOverridesProvider
	KnownRowCount              KnownRowCountProvider
	QueryLocker                QueryLocker
	ProfileConfirmer           ProfileConfirmer
	Now                        func() time.Time
}

// ToolBuilder 是负责动态创建有状态工具的函数
type ToolBuilder func(ctx ToolContext) Tool

var globalToolBuilders []ToolBuilder

// RegisterGlobalTool 用于各个包的 init() 方法向全局注册自己
func RegisterGlobalTool(builder ToolBuilder) {
	globalToolBuilders = append(globalToolBuilders, builder)
}

// LoadGlobalTools 实例化并注册所有在 init 中声明的工具
func (r *Registry) LoadGlobalTools(ctx ToolContext) {
	for _, builder := range globalToolBuilders {
		if tool := builder(ctx); tool != nil {
			r.Register(tool)
		}
	}
}

// NewRegistry 创建工具注册表
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// CloneFiltered 返回一个只包含指定名称工具的新 Registry
func (r *Registry) CloneFiltered(allowed []string) *Registry {
	filtered := NewRegistry()
	for _, name := range allowed {
		if tool, ok := r.tools[name]; ok {
			filtered.Register(tool)
		}
	}
	return filtered
}

// Register 注册工具
func (r *Registry) Register(tool Tool) {
	r.tools[tool.Name()] = tool
}

// Get 获取工具
func (r *Registry) Get(name string) (Tool, error) {
	tool, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool '%s' not registered", name)
	}
	return tool, nil
}

// HasTool 检查工具是否已注册
func (r *Registry) HasTool(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// ListTools 返回当前注册表中的工具快照。
func (r *Registry) ListTools() []Tool {
	items := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		items = append(items, tool)
	}
	return items
}

// GetToolSpecs 返回 provider-neutral 的工具定义快照。
func (r *Registry) GetToolSpecs() []ToolSpec {
	var specs []ToolSpec
	for _, tool := range r.tools {
		params := tool.Parameters()
		strict := false
		if strictTool, ok := tool.(StrictTool); ok {
			strict = strictTool.Strict()
		}
		specs = append(specs, ToolSpec{
			Type: "function",
			Function: FunctionSpec{
				Name:        tool.Name(),
				Description: tool.Description(),
				Strict:      strict,
				Parameters:  params,
			},
		})
	}
	return specs
}

// Execute 执行工具
func (r *Registry) Execute(name string, args json.RawMessage) (string, error) {
	tool, err := r.Get(name)
	if err != nil {
		return "", err
	}
	return tool.Execute(args)
}
