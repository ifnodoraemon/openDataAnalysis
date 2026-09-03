package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/service"
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

type ToolCapability struct {
	Mode                string `json:"mode"`
	RuntimeEnabled      bool   `json:"runtime_enabled"`
	Delegable           bool   `json:"delegable"`
	RequiresUserReceipt bool   `json:"requires_user_receipt,omitempty"`
	RunControl          string `json:"run_control,omitempty"`
	DeliveryBoundary    bool   `json:"delivery_boundary,omitempty"`
	EmitsReportPreview  bool   `json:"emits_report_preview,omitempty"`
}

type CapabilityTool interface {
	Capability() ToolCapability
}

type AvailabilityTool interface {
	CheckAvailability(context.Context) error
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
type RegistryFactory func(allowed []string) *Registry

type LiveQueryRequest struct {
	SourceID       string
	SQL            string
	TimeoutSeconds int
	MaxRows        int
}

type LiveQueryResult struct {
	SourceID string
	Dialect  string
	Columns  []string
	Rows     []map[string]interface{}
	RowCount int
}

type LiveSourceTable struct {
	Schema           string
	Name             string
	QualifiedName    string
	Kind             string
	RowCountEstimate int64
	Estimated        bool
	ProfileID        string
	SnapshotID       string
	Dialect          string
}

type LiveColumnFacts struct {
	Name         string
	DeclaredType string
}

type LiveTableDescription struct {
	SourceID         string
	Schema           string
	Name             string
	QualifiedName    string
	Dialect          string
	RowCountEstimate int64
	Estimated        bool
	ColumnCount      int
	Columns          []LiveColumnFacts
	Sample           *LiveQueryResult
	SampleRows       int
	Warnings         []string
}

type LiveQueryProvider func(ctx context.Context, req LiveQueryRequest) (*LiveQueryResult, error)
type LiveTablesProvider func(ctx context.Context, sourceID string) ([]LiveSourceTable, error)
type LiveTableDescribeProvider func(ctx context.Context, sourceID, schema, name string, sampleRows int) (*LiveTableDescription, error)

type ToolContext struct {
	Ingester                  *data.Ingester
	ReportState               *ReportState
	EditState                 *ReportEditState
	Memory                    any            // Type: *agent.WorkingMemory
	Subgoals                  SubgoalChecker // Instead of any, we use an interface to avoid circular imports
	DelegateRegistryFactory   RegistryFactory
	EmitFunc                  func(any) // Type: func(agent.RuntimeEvent)
	SessionID                 string
	WorkspaceID               string
	SessionSourcesProvider    SessionSourcesProvider
	PendingFileSourcesProvider PendingFileSourcesProvider
	ProfileDetailProvider     ProfileDetailProvider
	GovernanceProvider        GovernanceProvider
	QueryLocker               QueryLocker
	ProfileConfirmer          ProfileConfirmer
	LiveQueryProvider         LiveQueryProvider
	LiveTablesProvider        LiveTablesProvider
	LiveTableDescribeProvider LiveTableDescribeProvider
	SourceService             *service.SourceService
	SourceFileLookup          SourceFileLookup
	UploadLocker              UploadLocker
	Now                       func() time.Time
	FileService               *service.FileService
}

// SourceFileLookup resolves a file-upload source in the given workspace to its
// backing file id and human filename. It enforces workspace ownership.
type SourceFileLookup func(ctx context.Context, workspaceID, sourceID string) (fileID, filename string, err error)

// UploadLocker is the write side of the session import lock. Tools that write
// into the session analysis database must hold it for the whole mutation.
type UploadLocker interface {
	LockUpload()
	UnlockUpload()
}

// ToolBuilder 是负责动态创建有状态工具的函数
type ToolBuilder func(ctx ToolContext) Tool

var globalToolBuilders []ToolBuilder

// RegisterGlobalTool 用于各个包的 init() 方法向全局注册自己
func RegisterGlobalTool(builder ToolBuilder) {
	if builder == nil {
		panic("tool builder must not be nil")
	}
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

func (r *Registry) RuntimeToolNames(delegableOnly bool) []string {
	names := make([]string, 0, len(r.tools))
	for name, tool := range r.tools {
		provider, ok := tool.(CapabilityTool)
		if !ok {
			continue
		}
		capability := provider.Capability()
		if !capability.RuntimeEnabled || (delegableOnly && !capability.Delegable) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Register 注册工具
func (r *Registry) Register(tool Tool) {
	if r == nil || r.tools == nil {
		panic("tool registry is not initialized")
	}
	if tool == nil {
		panic("tool must not be nil")
	}
	name := tool.Name()
	if name == "" || name != strings.TrimSpace(name) {
		panic("tool name must be a non-empty exact value")
	}
	if _, exists := r.tools[name]; exists {
		panic(fmt.Sprintf("tool %q is already registered", name))
	}
	r.tools[name] = tool
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
