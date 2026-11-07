// Package dto berisi bentuk request dan response API. Model GORM tidak
// pernah di-bind langsung dari request (mencegah mass assignment).
package dto

import "time"

type LiveResponse struct {
	Status string `json:"status"`
}

type ReadyResponse struct {
	Status        string    `json:"status"`
	SchemaVersion int64     `json:"schema_version"`
	CheckedAt     time.Time `json:"checked_at"`
}
