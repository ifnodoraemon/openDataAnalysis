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
		StorageProvider:     "s3",
		RunBackend:          "inprocess",
		AnalysisStore:       "session_sqlite",
		PythonArtifactStore: "executor_local",
	}
	t.Cleanup(func() { config.Cfg = prev })

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected unsupported backend panic")
		}
		if !strings.Contains(recovered.(string), "STORAGE_PROVIDER=s3") {
			t.Fatalf("unexpected panic: %v", recovered)
		}
	}()

	ensureSupportedBackends()
}

func TestConfiguredObjectStorageUsesLocalDefault(t *testing.T) {
	prev := config.Cfg
	config.Cfg = &config.Config{StorageRoot: t.TempDir()}
	t.Cleanup(func() { config.Cfg = prev })

	if storage := configuredObjectStorage(); storage == nil {
		t.Fatal("expected local object storage")
	}
}
