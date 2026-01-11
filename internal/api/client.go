package api

import (
	"errors"
	"io"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

var (
	// ErrOwnershipTransferPending indicates that ownership transfer requires acceptance
	ErrOwnershipTransferPending = errors.New("ownership transfer pending acceptance")
)

// QuotaInfo represents storage quota information
type QuotaInfo struct {
	Total int64
	Used  int64
	Free  int64
}

// CloudClient defines the interface that all cloud provider clients must implement
type CloudClient interface {
	// Pre-flight check to verify sync folder structure
	PreFlightCheck() error

	// File operations
	ListFiles(folderID string) ([]*model.File, error)
	DownloadFile(fileID string, writer io.Writer) error
	UploadFile(folderID, name string, reader io.Reader, size int64) (*model.File, error)
	UpdateFile(fileID string, reader io.Reader, size int64) error
	DeleteFile(fileID string) error
	MoveFile(fileID, targetFolderID string) error

	// Folder operations
	ListFolders(parentID string) ([]*model.Folder, error)
	CreateFolder(parentID, name string) (*model.Folder, error)
	DeleteFolder(folderID string) error
	GetSyncFolderID() (string, error)
	GetDriveID() (string, error)
	CreateShortcut(parentID, name, targetID, targetDriveID string) (*model.File, error)

	// Permission management
	ShareFolder(folderID, email string, role string) error
	VerifyPermissions() error

	// Quota and metadata
	GetQuota() (*QuotaInfo, error)
	GetFileMetadata(fileID string) (*model.File, error)

	// Ownership transfer (Google Drive specific)
	TransferOwnership(fileID, newOwnerEmail string) error
	AcceptOwnership(fileID string) error

	// User information
	GetUserEmail() string
	GetUserIdentifier() string
}

// FileHasher defines methods for file hashing
type FileHasher interface {
	GetNativeHash(fileID string) (string, string, error) // returns hash, algorithm, error
	CalculateSHA256(reader io.Reader) (string, error)
}
