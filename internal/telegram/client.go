package telegram

import (
	"errors"
	"io"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

// Client represents a Telegram client
type Client struct {
	user *model.User
}

// NewClient creates a new Telegram client
func NewClient(user *model.User) (*Client, error) {
	return &Client{
		user: user,
	}, nil
}

// PreFlightCheck verifies the Telegram connection
func (c *Client) PreFlightCheck() error {
	// TODO: Implement Telegram pre-flight check
	return nil
}

// ListFiles lists files
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

// MoveFile moves a file (not supported)
func (c *Client) MoveFile(fileID, targetFolderID string) error {
	return errors.New("not supported")
}

// ListFolders lists folders (not applicable)
func (c *Client) ListFolders(parentID string) ([]*model.Folder, error) {
	return nil, nil
}

// CreateFolder creates a folder (not applicable)
func (c *Client) CreateFolder(parentID, name string) (*model.Folder, error) {
	return nil, errors.New("not supported")
}

// DeleteFolder deletes a folder (not applicable)
func (c *Client) DeleteFolder(folderID string) error {
	return errors.New("not supported")
}

// GetSyncFolderID returns the sync folder ID (not applicable)
func (c *Client) GetSyncFolderID() (string, error) {
	return "", nil
}

// ShareFolder shares a folder (not applicable)
func (c *Client) ShareFolder(folderID, email string, role string) error {
	return errors.New("not supported")
}

// VerifyPermissions verifies permissions (not applicable)
func (c *Client) VerifyPermissions() error {
	return nil
}

// GetQuota returns quota information
func (c *Client) GetQuota() (*api.QuotaInfo, error) {
	// Telegram doesn't have traditional quotas
	return &api.QuotaInfo{
		Total: -1,
		Used:  0,
		Free:  -1,
	}, nil
}

// GetFileMetadata retrieves file metadata
func (c *Client) GetFileMetadata(fileID string) (*model.File, error) {
	return nil, errors.New("not implemented")
}

// TransferOwnership is not supported
func (c *Client) TransferOwnership(fileID, newOwnerEmail string) error {
	return errors.New("not supported")
}

// GetUserEmail returns empty (Telegram uses phone)
func (c *Client) GetUserEmail() string {
	return ""
}

// GetUserIdentifier returns the user's phone
func (c *Client) GetUserIdentifier() string {
	return c.user.Phone
}
