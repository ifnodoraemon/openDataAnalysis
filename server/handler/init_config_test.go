package handler

import (
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
)

func TestEnsureSupportedBackendsRejectsUnsupportedProvider(t *testing.T) {
	prev := config.Cfg
	config.Cfg = &config.Config{
		MetadataStore:       "sqlite",
		StorageProvider:     "unsupported_provider",
		RunBackend:          "inprocess",
		AnalysisStore:       "session_sqlite",
		PythonArtifactStore: "object_storage",
	}
	t.Cleanup(func() { config.Cfg = prev })

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected unsupported backend panic")
		}
		if !strings.Contains(recovered.(string), "STORAGE_PROVIDER=unsupported_provider") {
			t.Fatalf("unexpected panic: %v", recovered)
		}
	}()

	ensureSupportedBackends()
}

func TestConfiguredObjectStorageRejectsMissingProvider(t *testing.T) {
	prev := config.Cfg
	config.Cfg = &config.Config{StorageRoot: t.TempDir()}
	t.Cleanup(func() { config.Cfg = prev })

	defer func() {
		if recover() == nil {
			t.Fatal("expected missing storage provider to panic")
		}
	}()
	configuredObjectStorage()
}
