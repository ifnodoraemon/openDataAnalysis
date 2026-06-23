package domain

import "time"

type SourceConfig struct {
	SourceID             string
	ConnectorType        SourceType
	ConfigJSON           string
	CredentialCiphertext []byte
	LastTestedAt         *time.Time
	LastTestStatus       string
	LastErrorMessage     *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}
