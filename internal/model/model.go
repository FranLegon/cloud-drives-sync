package model

import "time"

// User represents a single cloud account configured in the application.
// It's stored in the encrypted config.json.enc file.
type User struct {
	Provider     string `json:"provider"`
	Email        string `json:"email"`
	IsMain       bool   `json:"is_main"`
	RefreshToken string `json:"refresh_token"`
}

// File represents a file's metadata stored in the local encrypted database.
type File struct {
	FileID         string
	Provider       string
	OwnerEmail     string
	FileHash       string
	HashAlgorithm  string
	FileName       string
	FileSize       int64
	ParentFolderID string
	Path           string // The full, original case path from the sync root.
	NormalizedPath string // The lowercase, forward-slash path for matching.
	CreatedOn      time.Time
	LastModified   time.Time
	LastSynced     time.Time
}

// Folder represents a folder's metadata stored in the local encrypted database.
type Folder struct {
	FolderID       string
	Provider       string
	OwnerEmail     string
	FolderName     string
	ParentFolderID string
	Path           string
	NormalizedPath string
	LastSynced     time.Time
}

// StorageQuota holds information about a user's storage usage retrieved from the API.
type StorageQuota struct {
	TotalBytes     int64
	UsedBytes      int64
	RemainingBytes int64
	OwnerEmail     string
	Provider       string
}
