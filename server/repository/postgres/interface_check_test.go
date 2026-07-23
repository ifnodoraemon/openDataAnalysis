package postgres_test

import (
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/ifnodoraemon/openDataAnalysis/repository/postgres"
)

func TestInterfaceImplementations(t *testing.T) {
	var _ repository.ReportRepository = (*postgres.ReportRepository)(nil)
	var _ repository.MessageRepository = (*postgres.MessageRepository)(nil)
	var _ repository.DataSourceRepository = (*postgres.DataSourceRepository)(nil)
	var _ repository.SourceConfigRepository = (*postgres.SourceConfigRepository)(nil)
	var _ repository.SourceSnapshotRepository = (*postgres.SourceSnapshotRepository)(nil)
	var _ repository.SessionSourceBindingRepository = (*postgres.SessionSourceBindingRepository)(nil)
	var _ repository.SemanticProfileRepository = (*postgres.SemanticProfileRepository)(nil)
	var _ repository.SemanticConfirmationRepository = (*postgres.SemanticConfirmationRepository)(nil)
	var _ repository.SemanticAssetRepository = (*postgres.SemanticAssetRepository)(nil)
	var _ repository.AuditEventRepository = (*postgres.AuditEventRepository)(nil)
}
