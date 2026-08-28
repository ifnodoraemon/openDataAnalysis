package tools

import "context"

// ExecutionMetadata carries the persistence scope for side effects created by tools.
type ExecutionMetadata struct {
	UserID      string
	WorkspaceID string
	SessionID   string
	RunID       string
}

type executionMetadataKey struct{}

func WithExecutionMetadata(ctx context.Context, meta ExecutionMetadata) context.Context {
	return context.WithValue(ctx, executionMetadataKey{}, meta)
}

func ExecutionMetadataFromContext(ctx context.Context) ExecutionMetadata {
	if ctx == nil {
		return ExecutionMetadata{}
	}
	meta, _ := ctx.Value(executionMetadataKey{}).(ExecutionMetadata)
	return meta
}
