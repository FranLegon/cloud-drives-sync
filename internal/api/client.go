package api

import (
	"context"
	"io"

	"cloud-drives-sync/internal/model"
)

// CloudClient defines the interface for interacting with cloud storage providers
type CloudClient interface {
	// Account and authentication
	GetUserEmail(ctx context.Context) (string, error)

	// Folder operations
	FindFoldersByName(ctx context.Context, name string, includeTrash bool) ([]model.Folder, error)
	GetOrCreateFolder(ctx context.Context, name string, parentID string) (string, error)
	MoveFolder(ctx context.Context, folderID string, newParentID string) error
	ListFolders(ctx context.Context, parentID string, recursive bool) ([]model.Folder, error)

	// File operations
	ListFiles(ctx context.Context, folderID string, recursive bool) ([]model.File, error)
	DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error)
	UploadFile(ctx context.Context, parentFolderID string, fileName string, reader io.Reader) (*model.File, error)
	DeleteFile(ctx context.Context, fileID string) error

	// File hash operations
	GetFileHash(ctx context.Context, fileID string) (hash string, algorithm string, err error)

	// Permissions and sharing
	ShareFolder(ctx context.Context, folderID string, targetEmail string, role string) error
	CheckFolderPermission(ctx context.Context, folderID string, targetEmail string) (bool, error)

	// Storage quota
	GetQuota(ctx context.Context) (*model.QuotaInfo, error)

	// File transfer/ownership
	TransferFileOwnership(ctx context.Context, fileID string, targetEmail string) error

	// Export for proprietary formats
	ExportFile(ctx context.Context, fileID string, mimeType string) (io.ReadCloser, error)
}

// FileMetadata represents minimal file metadata for hash operations
type FileMetadata struct {
	ID            string
	Name          string
	Size          int64
	Hash          string
	HashAlgorithm string
	MimeType      string
}
