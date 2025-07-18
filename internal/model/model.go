package model

import "time"

// User represents a single user account for a cloud provider as defined in the
// configuration file. It holds authentication details and account roles.
type User struct {
	Provider     string `json:"provider"`
	Email        string `json:"email"`
	IsMain       bool   `json:"is_main"`
	RefreshToken string `json:"refresh_token"`
}

// File represents the metadata of a single file, designed to be stored in the
// local SQLite database. It mirrors the 'files' table schema.
type File struct {
	FileID         string    `db:"FileID"`
	Provider       string    `db:"Provider"`
	OwnerEmail     string    `db:"OwnerEmail"`
	FileHash       string    `db:"FileHash"`
	HashAlgorithm  string    `db:"HashAlgorithm"`
	FileName       string    `db:"FileName"`
	FileSize       int64     `db:"FileSize"`
	ParentFolderID string    `db:"ParentFolderID"`
	CreatedOn      time.Time `db:"CreatedOn"`
	LastModified   time.Time `db:"LastModified"`
	LastSynced     time.Time `db:"LastSynced"`
}

// Folder represents the metadata of a single folder, designed to be stored in the
// local SQLite database. It mirrors the 'folders' table schema and includes
// path information for cross-provider matching.
type Folder struct {
	FolderID       string    `db:"FolderID"`
	Provider       string    `db:"Provider"`
	OwnerEmail     string    `db:"OwnerEmail"`
	FolderName     string    `db:"FolderName"`
	ParentFolderID string    `db:"ParentFolderID"`
	Path           string    `db:"Path"`
	NormalizedPath string    `db:"NormalizedPath"`
	LastSynced     time.Time `db:"LastSynced"`
}
