package tools

import (
	"encoding/json"
	"testing"
)

type registryTestTool struct{}

func (registryTestTool) Name() string { return "strict_tool" }

func (registryTestTool) Description() string { return "test tool" }

func (registryTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
}

func (registryTestTool) Execute(args json.RawMessage) (string, error) { return "{}", nil }

func (registryTestTool) Strict() bool { return true }

func TestRegistryGetToolSpecsReturnsProviderNeutralSpecs(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(registryTestTool{})

	specs := reg.GetToolSpecs()
	if len(specs) != 1 {
		t.Fatalf("expected 1 tool spec, got %d", len(specs))
	}
	if specs[0].Type != "function" {
		t.Fatalf("unexpected tool type: %#v", specs[0])
	}
	if specs[0].Function.Name != "strict_tool" {
		t.Fatalf("unexpected tool name: %#v", specs[0])
	}
	if !specs[0].Function.Strict {
		t.Fatalf("expected strict flag to be preserved: %#v", specs[0])
	}
	if string(specs[0].Function.Parameters) != `{"type":"object","properties":{"x":{"type":"string"}}}` {
		t.Fatalf("unexpected parameters: %s", string(specs[0].Function.Parameters))
	}
}

func TestRegistryRejectsDuplicateToolNames(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(registryTestTool{})
	defer func() {
		if recover() == nil {
			t.Fatal("expected duplicate tool registration to panic")
		}
	}()
	reg.Register(registryTestTool{})
}

func TestRegistryRejectsUninitializedRegistry(t *testing.T) {
	t.Parallel()

	reg := &Registry{}
	defer func() {
		if recover() == nil {
			t.Fatal("expected uninitialized tool registry to panic")
		}
	}()
	reg.Register(registryTestTool{})
}
