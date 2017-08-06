// Package models holds database models for data access
package models

import "time"

// Ut4UpdatePackages holds available upgrade paths available
type Ut4UpdatePackages struct {
	ID          uint32
	FromVersion string
	ToVersion   string
	UpdateURL   string
	DateCreated time.Time
	IsDeleted   uint
}
