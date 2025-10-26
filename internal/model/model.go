package model

import "time"

// Provider represents a cloud storage provider
type Provider string

const (
	ProviderGoogle    Provider = "Google"
	ProviderMicrosoft Provider = "Microsoft"
)

// User represents a cloud storage account
type User struct {
	Provider     Provider `json:"provider"`
	Email        string   `json:"email"`
	IsMain       bool     `json:"is_main"`
	RefreshToken string   `json:"refresh_token"`
}

// File represents a file stored in the cloud
type File struct {
	FileID         string    `json:"file_id"`
	Provider       Provider  `json:"provider"`
	OwnerEmail     string    `json:"owner_email"`
	FileHash       string    `json:"file_hash"`
	HashAlgorithm  string    `json:"hash_algorithm"`
	FileName       string    `json:"file_name"`
	FileSize       int64     `json:"file_size"`
	ParentFolderID string    `json:"parent_folder_id"`
	CreatedOn      time.Time `json:"created_on"`
	LastModified   time.Time `json:"last_modified"`
	LastSynced     time.Time `json:"last_synced"`
}

// Folder represents a folder/directory in the cloud
type Folder struct {
	FolderID       string    `json:"folder_id"`
	Provider       Provider  `json:"provider"`
	OwnerEmail     string    `json:"owner_email"`
	FolderName     string    `json:"folder_name"`
	ParentFolderID string    `json:"parent_folder_id"`
	Path           string    `json:"path"`
	NormalizedPath string    `json:"normalized_path"`
	LastSynced     time.Time `json:"last_synced"`
}

// QuotaInfo represents storage quota information for an account
type QuotaInfo struct {
	Email          string   `json:"email"`
	Provider       Provider `json:"provider"`
	TotalBytes     int64    `json:"total_bytes"`
	UsedBytes      int64    `json:"used_bytes"`
	PercentageUsed float64  `json:"percentage_used"`
}
