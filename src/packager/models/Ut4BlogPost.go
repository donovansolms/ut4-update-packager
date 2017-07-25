// Package models holds database models for data access
package models

import "time"

// Ut4BlogPost is for data relating to the blog posts from UT4
type Ut4BlogPost struct {
	ID            uint32
	Title         string
	GUID          string
	DatePublished time.Time
	DateCreated   time.Time
	IsDeleted     uint
}
