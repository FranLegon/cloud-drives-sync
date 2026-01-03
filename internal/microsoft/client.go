package microsoft

import (
	"errors"
	"io"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

// Client represents a Microsoft OneDrive client
type Client struct {
	user *model.User
}

// NewClient creates a new Microsoft OneDrive client
func NewClient(user *model.User) (*Client, error) {
	return &Client{
		user: user,
	}, nil
}

// PreFlightCheck verifies the sync folder structure
func (c *Client) PreFlightCheck() error {
	// TODO: Implement Microsoft OneDrive pre-flight check
	return nil
}

// ListFiles lists files in a folder
func (c *Client) ListFiles(folderID string) ([]*model.File, error) {
	return nil, errors.New("not implemented")
}

// DownloadFile downloads a file
func (c *Client) DownloadFile(fileID string, writer io.Writer) error {
	return errors.New("not implemented")
}

// UploadFile uploads a file
func (c *Client) UploadFile(folderID, name string, reader io.Reader, size int64) (*model.File, error) {
	return nil, errors.New("not implemented")
}

// DeleteFile deletes a file
func (c *Client) DeleteFile(fileID string) error {
	return errors.New("not implemented")
}

// MoveFile moves a file
func (c *Client) MoveFile(fileID, targetFolderID string) error {
	return errors.New("not implemented")
}

// ListFolders lists folders
func (c *Client) ListFolders(parentID string) ([]*model.Folder, error) {
	return nil, errors.New("not implemented")
}

// CreateFolder creates a folder
func (c *Client) CreateFolder(parentID, name string) (*model.Folder, error) {
	return nil, errors.New("not implemented")
}

// DeleteFolder deletes a folder
func (c *Client) DeleteFolder(folderID string) error {
	return errors.New("not implemented")
}

// GetSyncFolderID returns the sync folder ID
func (c *Client) GetSyncFolderID() (string, error) {
	return "", errors.New("not implemented")
}

// ShareFolder shares a folder
func (c *Client) ShareFolder(folderID, email string, role string) error {
	return errors.New("not implemented")
}

// VerifyPermissions verifies permissions
func (c *Client) VerifyPermissions() error {
	return nil
}

// GetQuota returns quota information
func (c *Client) GetQuota() (*api.QuotaInfo, error) {
	return nil, errors.New("not implemented")
}

// GetFileMetadata retrieves file metadata
func (c *Client) GetFileMetadata(fileID string) (*model.File, error) {
	return nil, errors.New("not implemented")
}

// TransferOwnership transfers ownership
func (c *Client) TransferOwnership(fileID, newOwnerEmail string) error {
	return errors.New("not implemented - OneDrive doesn't support ownership transfer")
}

// GetUserEmail returns the user's email
func (c *Client) GetUserEmail() string {
	return c.user.Email
}

// GetUserIdentifier returns the user identifier
func (c *Client) GetUserIdentifier() string {
	return c.user.Email
}
