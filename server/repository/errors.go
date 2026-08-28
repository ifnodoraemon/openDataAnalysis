package repository

import "errors"

// ErrNotFound is the repository-layer identity for an absent record. Storage
// implementations wrap this sentinel instead of encoding absence in text.
var ErrNotFound = errors.New("repository record not found")
