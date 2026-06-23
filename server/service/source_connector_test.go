package service

import (
	"context"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

func TestSourceConnectorRegistryReturnsRegisteredConnector(t *testing.T) {
	t.Parallel()

	registry := NewSourceConnectorRegistry()
	connector := fakeSourceConnector{sourceType: domain.SourceTypePostgresConnection}
	registry.Register(connector)

	got, err := registry.Get(domain.SourceTypePostgresConnection)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Type() != domain.SourceTypePostgresConnection {
		t.Fatalf("expected postgres connector, got %q", got.Type())
	}

	_, err = registry.Get(domain.SourceType("mysql_connection"))
	if err == nil || !strings.Contains(err.Error(), "unsupported data source type") {
		t.Fatalf("expected unsupported source type error, got %v", err)
	}
}

func TestSourceConnectorRegistryReturnsSpecs(t *testing.T) {
	t.Parallel()

	registry := NewSourceConnectorRegistry()
	registry.Register(fakeSourceConnector{
		sourceType: domain.SourceTypePostgresConnection,
		spec: SourceConnectorSpec{
			SourceType:   domain.SourceTypePostgresConnection,
			Label:        "PostgreSQL",
			Category:     "sql",
			Configurable: true,
		},
	})

	specs := registry.Specs()
	if len(specs) != 1 {
		t.Fatalf("expected one spec, got %d", len(specs))
	}
	if specs[0].SourceType != domain.SourceTypePostgresConnection || !specs[0].Configurable {
		t.Fatalf("unexpected spec: %#v", specs[0])
	}
}

func TestSourceScopedFileTableNameAvoidsCrossSourceCollision(t *testing.T) {
	t.Parallel()

	first := SourceScopedFileTableName("sales.csv", "ds_alpha_12345678")
	second := SourceScopedFileTableName("sales.csv", "ds_beta_87654321")
	if first == second {
		t.Fatalf("expected source-scoped table names to differ, both %q", first)
	}
	if first != "sales__12345678" || second != "sales__87654321" {
		t.Fatalf("unexpected scoped table names: %q %q", first, second)
	}
}

type fakeSourceConnector struct {
	sourceType domain.SourceType
	spec       SourceConnectorSpec
}

func (c fakeSourceConnector) Type() domain.SourceType { return c.sourceType }

func (c fakeSourceConnector) Spec() SourceConnectorSpec {
	if c.spec.SourceType == "" {
		return SourceConnectorSpec{SourceType: c.sourceType}
	}
	return c.spec
}

func (c fakeSourceConnector) NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error) {
	return nil, nil
}

func (c fakeSourceConnector) PublicConfig(ctx context.Context, sourceID string) (map[string]interface{}, error) {
	return nil, nil
}

func (c fakeSourceConnector) Test(ctx context.Context, req SourceTestRequest) (map[string]interface{}, error) {
	return map[string]interface{}{"success": true}, nil
}

func (c fakeSourceConnector) Catalog(ctx context.Context, sourceID string) ([]SourceCatalogObject, error) {
	return nil, nil
}

func (c fakeSourceConnector) Import(ctx context.Context, req SourceImportRequest) (*SnapshotImportResult, error) {
	return nil, nil
}
