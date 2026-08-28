package postgres

import (
	"errors"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/jackc/pgx/v5"
)

func normalizeLookupError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", repository.ErrNotFound, err)
	}
	return err
}
