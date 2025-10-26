package microsoft

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/crypto"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/drives"
	graphmodels "github.com/microsoftgraph/msgraph-sdk-go/models"
	"golang.org/x/oauth2"
)

// Client implements the CloudClient interface for Microsoft OneDrive
type Client struct {
	graphClient *msgraphsdk.GraphServiceClient
	email       string
	driveID     string
	tokenSource oauth2.TokenSource
	log         *logger.Logger
	maxRetries  int
	retryDelay  time.Duration
}

// tokenCredential wraps oauth2.TokenSource to implement azidentity.TokenCredential
type tokenCredential struct {
	tokenSource oauth2.TokenSource
}

// GetToken implements azcore.TokenCredential
func (tc *tokenCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	token, err := tc.tokenSource.Token()
	if err != nil {
		return azcore.AccessToken{}, err
	}

	return azcore.AccessToken{
		Token:     token.AccessToken,
		ExpiresOn: token.Expiry,
	}, nil
}

// NewClient creates a new Microsoft OneDrive client
func NewClient(ctx context.Context, tokenSource oauth2.TokenSource) (api.CloudClient, error) {
	// Create token credential wrapper
	cred := &tokenCredential{tokenSource: tokenSource}

	// Create Graph client
	graphClient, err := msgraphsdk.NewGraphServiceClientWithCredentials(cred, []string{})
	if err != nil {
		return nil, fmt.Errorf("failed to create Graph client: %w", err)
	}

	client := &Client{
		graphClient: graphClient,
		tokenSource: tokenSource,
		log:         logger.New().WithPrefix("Microsoft"),
		maxRetries:  3,
		retryDelay:  time.Second,
	}

	// Get user email
	email, err := client.GetUserEmail(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get user email: %w", err)
	}
	client.email = email

	// Get default drive ID
	driveID, err := client.getDefaultDriveID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get drive ID: %w", err)
	}
	client.driveID = driveID

	client.log = logger.New().WithPrefix(fmt.Sprintf("Microsoft:%s", email))

	return client, nil
}

// GetUserEmail returns the email address of the authenticated user
func (c *Client) GetUserEmail(ctx context.Context) (string, error) {
	user, err := c.graphClient.Me().Get(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get user info: %w", err)
	}

	if user.GetUserPrincipalName() != nil {
		return *user.GetUserPrincipalName(), nil
	}

	return "", fmt.Errorf("user email not available")
}

// getDefaultDriveID gets the default drive ID for the user
func (c *Client) getDefaultDriveID(ctx context.Context) (string, error) {
	drive, err := c.graphClient.Me().Drive().Get(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get drive: %w", err)
	}

	if drive.GetId() != nil {
		return *drive.GetId(), nil
	}

	return "", fmt.Errorf("drive ID not available")
}

// FindFoldersByName finds all folders with the given name
func (c *Client) FindFoldersByName(ctx context.Context, name string, includeTrash bool) ([]model.Folder, error) {
	// Search for folders with the given name using Microsoft Graph search syntax
	query := name

	top := int32(999)
	requestConfig := &drives.ItemSearchWithQRequestBuilderGetRequestConfiguration{
		QueryParameters: &drives.ItemSearchWithQRequestBuilderGetQueryParameters{
			Top: &top,
		},
	}

	items, err := c.graphClient.Drives().ByDriveId(c.driveID).SearchWithQ(&query).Get(ctx, requestConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to search for folders: %w", err)
	}

	c.log.Info("Search returned %d items for query: %s", len(items.GetValue()), name)

	var folders []model.Folder
	for _, item := range items.GetValue() {
		// Only include folders
		if item.GetFolder() == nil {
			c.log.Info("Skipping non-folder item: %s", *item.GetName())
			continue
		}

		// Skip trashed items if requested
		if !includeTrash && item.GetDeleted() != nil {
			continue
		}

		// Check if name matches exactly (search might return partial matches)
		if item.GetName() == nil || *item.GetName() != name {
			c.log.Info("Skipping folder with non-matching name: %s (wanted: %s)", *item.GetName(), name)
			continue
		}

		parentID := ""
		if item.GetParentReference() != nil && item.GetParentReference().GetId() != nil {
			parentID = *item.GetParentReference().GetId()
		}

		folders = append(folders, model.Folder{
			FolderID:       *item.GetId(),
			Provider:       model.ProviderMicrosoft,
			OwnerEmail:     c.email,
			FolderName:     *item.GetName(),
			ParentFolderID: parentID,
		})
	}

	c.log.Info("Found %d matching folder(s)", len(folders))
	return folders, nil
}

// GetOrCreateFolder gets or creates a folder with the given name and parent
func (c *Client) GetOrCreateFolder(ctx context.Context, name string, parentID string) (string, error) {
	// If no parent specified, use root
	if parentID == "" {
		parentID = "root"
	}

	// List children of parent to find existing folder
	items, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(parentID).Children().Get(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to list folder children: %w", err)
	}

	for _, item := range items.GetValue() {
		if item.GetFolder() != nil && item.GetName() != nil && *item.GetName() == name {
			return *item.GetId(), nil
		}
	}

	// Create new folder
	newFolder := graphmodels.NewDriveItem()
	newFolder.SetName(&name)
	newFolder.SetFolder(graphmodels.NewFolder())

	created, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(parentID).Children().Post(ctx, newFolder, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create folder: %w", err)
	}

	c.log.Info("Created folder '%s' (ID: %s)", name, *created.GetId())
	return *created.GetId(), nil
}

// MoveFolder moves a folder to a new parent
func (c *Client) MoveFolder(ctx context.Context, folderID string, newParentID string) error {
	if newParentID == "" {
		newParentID = "root"
	}

	// Update the parent reference
	update := graphmodels.NewDriveItem()
	parentRef := graphmodels.NewItemReference()
	parentRef.SetId(&newParentID)
	update.SetParentReference(parentRef)

	_, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(folderID).Patch(ctx, update, nil)
	if err != nil {
		return fmt.Errorf("failed to move folder: %w", err)
	}

	c.log.Info("Moved folder %s to parent %s", folderID, newParentID)
	return nil
}

// ListFolders lists all folders within a parent folder
func (c *Client) ListFolders(ctx context.Context, parentID string, recursive bool) ([]model.Folder, error) {
	var allFolders []model.Folder

	if parentID == "" {
		parentID = "root"
	}

	folders, err := c.listFoldersInParent(ctx, parentID, "")
	if err != nil {
		return nil, err
	}
	allFolders = append(allFolders, folders...)

	if recursive {
		for _, folder := range folders {
			subFolders, err := c.ListFolders(ctx, folder.FolderID, true)
			if err != nil {
				return nil, err
			}
			allFolders = append(allFolders, subFolders...)
		}
	}

	return allFolders, nil
}

// listFoldersInParent is a helper to list folders in a specific parent
func (c *Client) listFoldersInParent(ctx context.Context, parentID string, pathPrefix string) ([]model.Folder, error) {
	items, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(parentID).Children().Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list folders: %w", err)
	}

	var folders []model.Folder
	for _, item := range items.GetValue() {
		// Only include folders
		if item.GetFolder() == nil {
			continue
		}

		path := pathPrefix + "/" + *item.GetName()
		normalizedPath := logger.NormalizePath(path)

		folders = append(folders, model.Folder{
			FolderID:       *item.GetId(),
			Provider:       model.ProviderMicrosoft,
			OwnerEmail:     c.email,
			FolderName:     *item.GetName(),
			ParentFolderID: parentID,
			Path:           path,
			NormalizedPath: normalizedPath,
			LastSynced:     time.Now(),
		})
	}

	return folders, nil
}

// ListFiles lists all files within a folder
func (c *Client) ListFiles(ctx context.Context, folderID string, recursive bool) ([]model.File, error) {
	var allFiles []model.File

	if folderID == "" {
		folderID = "root"
	}

	files, err := c.listFilesInParent(ctx, folderID)
	if err != nil {
		return nil, err
	}
	allFiles = append(allFiles, files...)

	if recursive {
		folders, err := c.listFoldersInParent(ctx, folderID, "")
		if err != nil {
			return nil, err
		}

		for _, folder := range folders {
			subFiles, err := c.ListFiles(ctx, folder.FolderID, true)
			if err != nil {
				return nil, err
			}
			allFiles = append(allFiles, subFiles...)
		}
	}

	return allFiles, nil
}

// listFilesInParent is a helper to list files in a specific parent
func (c *Client) listFilesInParent(ctx context.Context, parentID string) ([]model.File, error) {
	items, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(parentID).Children().Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	var files []model.File
	for _, item := range items.GetValue() {
		// Skip folders
		if item.GetFolder() != nil {
			continue
		}

		hash := ""
		hashAlgo := "quickXorHash"

		// Get hash from file metadata
		if item.GetFile() != nil && item.GetFile().GetHashes() != nil {
			if item.GetFile().GetHashes().GetQuickXorHash() != nil {
				hash = *item.GetFile().GetHashes().GetQuickXorHash()
			} else if item.GetFile().GetHashes().GetSha1Hash() != nil {
				hash = *item.GetFile().GetHashes().GetSha1Hash()
				hashAlgo = "SHA1"
			}
		}

		// If no hash available, we'll need to download and compute
		if hash == "" {
			c.log.Warning("File '%s' (ID: %s) has no hash, will need fallback", *item.GetName(), *item.GetId())
			hashAlgo = "SHA256"
		}

		size := int64(0)
		if item.GetSize() != nil {
			size = *item.GetSize()
		}

		createdTime := time.Now()
		if item.GetCreatedDateTime() != nil {
			createdTime = *item.GetCreatedDateTime()
		}

		modifiedTime := time.Now()
		if item.GetLastModifiedDateTime() != nil {
			modifiedTime = *item.GetLastModifiedDateTime()
		}

		files = append(files, model.File{
			FileID:         *item.GetId(),
			Provider:       model.ProviderMicrosoft,
			OwnerEmail:     c.email,
			FileHash:       hash,
			HashAlgorithm:  hashAlgo,
			FileName:       *item.GetName(),
			FileSize:       size,
			ParentFolderID: parentID,
			CreatedOn:      createdTime,
			LastModified:   modifiedTime,
			LastSynced:     time.Now(),
		})
	}

	return files, nil
}

// DownloadFile downloads a file from OneDrive
func (c *Client) DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error) {
	// Download content directly using Graph SDK's content endpoint
	stream, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(fileID).Content().Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}

	return io.NopCloser(bytes.NewReader(stream)), nil
}

// UploadFile uploads a file to OneDrive
func (c *Client) UploadFile(ctx context.Context, parentFolderID string, fileName string, reader io.Reader) (*model.File, error) {
	if parentFolderID == "" {
		parentFolderID = "root"
	}

	// Read all data (for small files)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read file data: %w", err)
	}

	// For simplicity, first create the file metadata, then upload content
	// Create file item
	fileItem := graphmodels.NewDriveItem()
	fileItem.SetName(&fileName)
	fileItem.SetFile(graphmodels.NewFile())

	var createdItem graphmodels.DriveItemable
	if parentFolderID == "root" {
		// Use drive root children
		createdItem, err = c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId("root").Children().Post(ctx, fileItem, nil)
	} else {
		createdItem, err = c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(parentFolderID).Children().Post(ctx, fileItem, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}

	// Upload content
	_, err = c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(*createdItem.GetId()).Content().Put(ctx, data, nil)
	if err != nil {
		// Try to clean up the created item
		_ = c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(*createdItem.GetId()).Delete(ctx, nil)
		return nil, fmt.Errorf("failed to upload file content: %w", err)
	}

	// Refresh metadata after upload
	created, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(*createdItem.GetId()).Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get updated file metadata: %w", err)
	}

	c.log.Info("Uploaded file '%s' (ID: %s)", fileName, *created.GetId())

	hash := ""
	hashAlgo := "quickXorHash"
	if created.GetFile() != nil && created.GetFile().GetHashes() != nil && created.GetFile().GetHashes().GetQuickXorHash() != nil {
		hash = *created.GetFile().GetHashes().GetQuickXorHash()
	}

	size := int64(0)
	if created.GetSize() != nil {
		size = *created.GetSize()
	}

	createdTime := time.Now()
	if created.GetCreatedDateTime() != nil {
		createdTime = *created.GetCreatedDateTime()
	}

	modifiedTime := time.Now()
	if created.GetLastModifiedDateTime() != nil {
		modifiedTime = *created.GetLastModifiedDateTime()
	}

	return &model.File{
		FileID:         *created.GetId(),
		Provider:       model.ProviderMicrosoft,
		OwnerEmail:     c.email,
		FileHash:       hash,
		HashAlgorithm:  hashAlgo,
		FileName:       *created.GetName(),
		FileSize:       size,
		ParentFolderID: parentFolderID,
		CreatedOn:      createdTime,
		LastModified:   modifiedTime,
		LastSynced:     time.Now(),
	}, nil
}

// DeleteFile deletes a file from OneDrive
func (c *Client) DeleteFile(ctx context.Context, fileID string) error {
	err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(fileID).Delete(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	c.log.Info("Deleted file ID: %s", fileID)
	return nil
}

// GetFileHash retrieves the hash of a file
func (c *Client) GetFileHash(ctx context.Context, fileID string) (hash string, algorithm string, err error) {
	item, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(fileID).Get(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get file metadata: %w", err)
	}

	// Try to get quickXorHash first
	if item.GetFile() != nil && item.GetFile().GetHashes() != nil {
		if item.GetFile().GetHashes().GetQuickXorHash() != nil {
			return *item.GetFile().GetHashes().GetQuickXorHash(), "quickXorHash", nil
		}
		if item.GetFile().GetHashes().GetSha1Hash() != nil {
			return *item.GetFile().GetHashes().GetSha1Hash(), "SHA1", nil
		}
	}

	// Fallback: download and compute SHA256
	c.log.Warning("File %s requires download for hashing", fileID)

	stream, err := c.DownloadFile(ctx, fileID)
	if err != nil {
		return "", "", fmt.Errorf("failed to download file for hashing: %w", err)
	}
	defer stream.Close()

	hashValue, err := crypto.HashFile(stream)
	if err != nil {
		return "", "", fmt.Errorf("failed to hash file: %w", err)
	}

	return hashValue, "SHA256", nil
}

// ExportFile exports a file (OneDrive doesn't need special export for most files)
func (c *Client) ExportFile(ctx context.Context, fileID string, mimeType string) (io.ReadCloser, error) {
	// For OneDrive, most files can be downloaded directly
	// Office files can be converted if needed
	return c.DownloadFile(ctx, fileID)
}

// ShareFolder shares a folder with another user
func (c *Client) ShareFolder(ctx context.Context, folderID string, targetEmail string, role string) error {
	// Map role to Microsoft Graph roles
	graphRole := "read"
	if role == "writer" || role == "write" {
		graphRole = "write"
	}

	// Create the invite request body
	recipient := graphmodels.NewDriveRecipient()
	recipient.SetEmail(&targetEmail)

	postBody := drives.NewItemItemsItemInvitePostRequestBody()
	postBody.SetRecipients([]graphmodels.DriveRecipientable{recipient})
	postBody.SetRoles([]string{graphRole})
	sendInvitation := true
	postBody.SetSendInvitation(&sendInvitation)
	requireSignIn := true
	postBody.SetRequireSignIn(&requireSignIn)

	// Send the invitation
	result, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(folderID).Invite().Post(ctx, postBody, nil)
	if err != nil {
		return fmt.Errorf("failed to share folder: %w", err)
	}

	// Log the result details
	if result != nil && len(result.GetValue()) > 0 {
		for _, perm := range result.GetValue() {
			permID := "unknown"
			if perm.GetId() != nil {
				permID = *perm.GetId()
			}
			c.log.Info("Created permission ID: %s for folder %s", permID, folderID)
		}
	} else {
		c.log.Warning("Invite API returned no permissions - this may indicate the sharing failed silently")
	}

	c.log.Info("Shared folder %s with %s (role: %s)", folderID, targetEmail, role)
	return nil
}

// CheckFolderPermission checks if a user has access to a folder
func (c *Client) CheckFolderPermission(ctx context.Context, folderID string, targetEmail string) (bool, error) {
	permissions, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(folderID).Permissions().Get(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to list permissions: %w", err)
	}

	for _, perm := range permissions.GetValue() {
		if perm.GetGrantedToV2() != nil && perm.GetGrantedToV2().GetUser() != nil {
			// Try to match by display name or ID since Email might not be available
			user := perm.GetGrantedToV2().GetUser()
			if user.GetDisplayName() != nil && *user.GetDisplayName() == targetEmail {
				return true, nil
			}
			if user.GetId() != nil && *user.GetId() == targetEmail {
				return true, nil
			}
		}
	}

	return false, nil
}

// GetQuota retrieves storage quota information
func (c *Client) GetQuota(ctx context.Context) (*model.QuotaInfo, error) {
	drive, err := c.graphClient.Drives().ByDriveId(c.driveID).Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get drive info: %w", err)
	}

	if drive.GetQuota() == nil {
		return nil, fmt.Errorf("quota information not available")
	}

	total := int64(0)
	if drive.GetQuota().GetTotal() != nil {
		total = *drive.GetQuota().GetTotal()
	}

	used := int64(0)
	if drive.GetQuota().GetUsed() != nil {
		used = *drive.GetQuota().GetUsed()
	}

	percentage := 0.0
	if total > 0 {
		percentage = (float64(used) / float64(total)) * 100
	}

	return &model.QuotaInfo{
		Email:          c.email,
		Provider:       model.ProviderMicrosoft,
		TotalBytes:     total,
		UsedBytes:      used,
		PercentageUsed: percentage,
	}, nil
}

// TransferFileOwnership attempts to transfer file ownership (not fully supported)
func (c *Client) TransferFileOwnership(ctx context.Context, fileID string, targetEmail string) error {
	// OneDrive/SharePoint doesn't support direct ownership transfer via API for personal accounts
	// Return error to trigger fallback
	return fmt.Errorf("ownership transfer not supported by OneDrive API, use download/upload fallback")
}

// ptrInt32 is a helper to get pointer to int32
func ptrInt32(i int32) *int32 {
	return &i
}

// computeQuickXorHash computes QuickXorHash (Microsoft's hash algorithm)
func computeQuickXorHash(data []byte) string {
	// Simplified implementation - real QuickXorHash is more complex
	// For production, use proper implementation
	hash := make([]byte, 20)
	for i, b := range data {
		hash[i%20] ^= b
	}
	return base64.StdEncoding.EncodeToString(hash)
}
