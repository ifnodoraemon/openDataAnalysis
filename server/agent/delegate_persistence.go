package agent

import "context"

type ChildRunStart struct {
	ParentRunID  string
	RoleName     string
	InputMessage string
	GoalID       string
	AllowedTools []string
}

type DelegateRunPersistence interface {
	StartChildRun(ctx context.Context, input ChildRunStart) (string, error)
	AppendChildEvent(ctx context.Context, childRunID string, ev RuntimeEvent) error
	UpdateChildRunStatus(ctx context.Context, childRunID string, status string, errMsg *string) error
	UpdateChildRunSummary(ctx context.Context, childRunID, summary string) error
	// UpdateChildRunTokens 记录子代理累计的 prompt/completion token 消耗。
	// 实现方可以选择将数据嵌入日志事件或写入持久化存储。
	// 若持久化无意义则可实现为空操作。
	UpdateChildRunTokens(ctx context.Context, childRunID string, promptTokens, completionTokens int) error
}

type delegatePersistenceKey struct{}

func WithDelegateRunPersistence(ctx context.Context, persistence DelegateRunPersistence) context.Context {
	if persistence == nil {
		return ctx
	}
	return context.WithValue(ctx, delegatePersistenceKey{}, persistence)
}

func DelegateRunPersistenceFromContext(ctx context.Context) DelegateRunPersistence {
	persistence, _ := ctx.Value(delegatePersistenceKey{}).(DelegateRunPersistence)
	return persistence
}
