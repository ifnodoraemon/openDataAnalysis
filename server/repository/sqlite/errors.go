package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

func normalizeLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %w", repository.ErrNotFound, err)
	}
	return err
}
