package api

import (
	"cloud-drives-sync/internal/model"
	"io"
)

// CloudClient defines the common interface for interacting with any cloud storage provider.
// This abstraction is key to making the business logic in `task_runner` provider-agnostic.
type CloudClient interface {
	// GetProviderName returns the name of the provider (e.g., "Google").
	GetProviderName() string
	// GetUserEmail retrieves the primary email address of the authenticated user.
	GetUserEmail() (string, error)
	// GetAbout retrieves storage quota and usage information.
	GetAbout() (*model.StorageQuota, error)
	// PreFlightCheck verifies that the `synched-cloud-drives` folder exists and is correctly configured.
	// It returns the folder's ID if found, or an empty string if not found. An error is returned for ambiguities.
	PreFlightCheck() (string, error)
	// CreateRootSyncFolder creates the `synched-cloud-drives` folder in the account's root.
	CreateRootSyncFolder() (string, error)
	// ListFolders recursively lists all subfolders within a given folder ID, invoking the callback for each.
	ListFolders(folderID string, parentPath string, callback func(model.Folder) error) error
	// ListFiles recursively lists all files within a given folder ID, invoking the callback for each.
	ListFiles(folderID string, parentPath string, callback func(model.File) error) error
	// CreateFolder creates a new folder within a parent folder.
	CreateFolder(parentFolderID, name string) (*model.Folder, error)
	// DownloadFile downloads a file's content by its ID.
	DownloadFile(fileID string) (io.ReadCloser, int64, error)
	// ExportFile downloads a proprietary file format (like Google Docs) as a standard type.
	ExportFile(fileID, mimeType string) (io.ReadCloser, int64, error)
	// UploadFile uploads a file using a streaming approach to minimize memory usage.
	UploadFile(parentFolderID, name string, content io.Reader, size int64) (*model.File, error)
	// DeleteFile moves a file to the provider's trash or recycle bin.
	DeleteFile(fileID string) error
	// Share grants "editor" (write) permissions to a user for a specific folder.
	Share(folderID, emailAddress string) (string, error)
	// CheckShare verifies if a specific permission ID is still valid for a folder.
	CheckShare(folderID, permissionID string) (bool, error)
	// TransferOwnership attempts a native API call to transfer file ownership to another user.
	// Returns true on success, false if the operation is unsupported or fails.
	TransferOwnership(fileID, emailAddress string) (bool, error)
	// MoveFile moves a file from one parent folder to another.
	MoveFile(fileID, currentParentID, newParentFolderID string) error
}
