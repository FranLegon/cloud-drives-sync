package api

import (
	"context"
	"io"
	"time"
)

// SyncFolderName is the required name for the root directory in each main
// account that the tool is allowed to operate within.
const SyncFolderName = "synched-cloud-drives"

// FileInfo contains standardized metadata for a file retrieved from a cloud API.
type FileInfo struct {
	ID              string
	Name            string
	Size            int64
	ParentFolderIDs []string
	CreatedTime     time.Time
	ModifiedTime    time.Time
	Owner           string
	Hash            string
	HashAlgorithm   string            // e.g., "md5", "quickXorHash", "sha256"
	IsProprietary   bool              // e.g., true for Google Docs, Google Sheets
	ExportLinks     map[string]string // MimeType -> URL for proprietary files
}

// FolderInfo contains standardized metadata for a folder retrieved from a cloud API.
type FolderInfo struct {
	ID              string
	Name            string
	ParentFolderIDs []string
	Owner           string
}

// QuotaInfo represents the storage quota for an account.
type QuotaInfo struct {
	TotalBytes int64
	UsedBytes  int64
	FreeBytes  int64
}

// CloudClient defines a universal interface for interacting with different cloud
// storage providers. All provider-specific clients (e.g., Google, Microsoft)
// must implement this interface.
type CloudClient interface {
	// PreflightCheck verifies that exactly one 'synched-cloud-drives' folder exists
	// at the root of the main account. It returns the ID of this folder.
	PreflightCheck(ctx context.Context) (string, error)

	// GetUserInfo retrieves basic information (email) about the authenticated user.
	GetUserInfo(ctx context.Context) (string, error)

	// ListAllFilesAndFolders recursively scans and returns all items within the sync folder.
	ListAllFilesAndFolders(ctx context.Context, rootFolderID string) ([]FileInfo, []FolderInfo, error)

	// CreateFolder creates a new folder within a specified parent folder.
	CreateFolder(ctx context.Context, parentFolderID, folderName string) (*FolderInfo, error)

	// UploadFile streams a file to the specified parent folder. It requires the file
	// size for providers that use resumable uploads.
	UploadFile(ctx context.Context, parentFolderID, fileName string, fileSize int64, content io.Reader) (*FileInfo, error)

	// DownloadFile downloads a standard file's content and returns a reader stream.
	DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error)

	// ExportFile downloads a proprietary file (like a Google Doc) by exporting it to
	// a standard format (e.g., application/pdf).
	ExportFile(ctx context.Context, fileID, mimeType string) (io.ReadCloser, error)

	// DeleteItem permanently deletes a file or folder. This action is irreversible.
	DeleteItem(ctx context.Context, itemID string) error

	// ShareFolder grants 'editor' (or equivalent) permissions to a user for a folder.
	ShareFolder(ctx context.Context, folderID, emailAddress string) error

	// GetStorageQuota returns the storage usage and total capacity for the account.
	GetStorageQuota(ctx context.Context) (*QuotaInfo, error)

	// MoveFile changes the parent of a file. This is used for moving files within
	// the same account. Returns the new parent ID if successful.
	MoveFile(ctx context.Context, fileID, newParentFolderID, oldParentFolderID string) error

	// TransferOwnership attempts to change the owner of a file to another user.
	// This may not be supported by all providers or account types.
	TransferOwnership(ctx context.Context, fileID, userEmail string) (bool, error)
}
