// Package models holds database models for data access
package models

import "time"

// Ut4VersionHashes is for data relating to previous versions' hashes
type Ut4VersionHashes struct {
	ID          uint32
	Version     string
	Hashes      string // JSON data
	DateCreated time.Time
	IsDeleted   uint
}
