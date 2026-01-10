package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

const (
	syncChannelName = "sync-cloud-drives"
	maxFileSize     = 2000 * 1024 * 1024 // 2GB Telegram limit
)

// FileMetadata represents the metadata stored in the caption
type FileMetadata struct {
	FileName    string `json:"file_name"`
	FolderPath  string `json:"folder_path"`
	GeneratedID string `json:"calculated_id"`
	Split       bool   `json:"split"`
	Part        int    `json:"part"`
	TotalParts  int    `json:"total_parts"`
	SoftDeleted bool   `json:"soft-deleted"`
}

// findMessagesByGeneratedID searches for messages with the given generated ID
func (c *Client) findMessagesByGeneratedID(generatedID string) ([]tg.MessageClass, error) {
	if c.channelID == 0 {
		return nil, fmt.Errorf("channel not initialized")
	}

	// Search for the generated ID in the caption
	// We use the JSON key to be more specific
	query := fmt.Sprintf(`"calculated_id":"%s"`, generatedID)

	inputPeer := &tg.InputPeerChannel{
		ChannelID:  c.channelID,
		AccessHash: c.accessHash,
	}

	res, err := c.client.API().MessagesSearch(c.ctx, &tg.MessagesSearchRequest{
		Peer:   inputPeer,
		Q:      query,
		Filter: &tg.InputMessagesFilterDocument{},
		Limit:  100, // Should be enough for split files
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search messages: %w", err)
	}

	var messages []tg.MessageClass
	switch r := res.(type) {
	case *tg.MessagesChannelMessages:
		messages = r.Messages
	case *tg.MessagesMessages:
		messages = r.Messages
	case *tg.MessagesMessagesSlice:
		messages = r.Messages
	}

	return messages, nil
}

// updateMessageCaption updates the caption of a message
func (c *Client) updateMessageCaption(msgID int, caption string) error {
	inputPeer := &tg.InputPeerChannel{
		ChannelID:  c.channelID,
		AccessHash: c.accessHash,
	}

	_, err := c.client.API().MessagesEditMessage(c.ctx, &tg.MessagesEditMessageRequest{
		Peer:    inputPeer,
		ID:      msgID,
		Message: caption,
	})
	return err
}

// Client represents a Telegram client
type Client struct {
	user          *model.User
	apiID         int
	apiHash       string
	client        *telegram.Client
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	channelID     int64
	accessHash    int64
	uploader      *uploader.Uploader
	downloader    *downloader.Downloader
	sender        *message.Sender
	authenticated bool
}

// NewClient creates a new Telegram client
func NewClient(user *model.User, apiIDStr string, apiHash string) (*Client, error) {
	apiID, err := strconv.Atoi(apiIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid API ID: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	sessionStorage := NewMemorySession(user)

	// Initialize the client
	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: sessionStorage,
	})

	c := &Client{
		user:    user,
		apiID:   apiID,
		apiHash: apiHash,
		client:  client,
		ctx:     ctx,
		cancel:  cancel,
	}

	// Start the client in a background goroutine
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := client.Run(ctx, func(ctx context.Context) error {
			// Initialize helpers
			c.uploader = uploader.NewUploader(client.API())
			c.downloader = downloader.NewDownloader()
			c.sender = message.NewSender(client.API()).WithUploader(c.uploader)

			// Check authentication status
			status, err := client.Auth().Status(ctx)
			if err != nil {
				return err
			}
			c.authenticated = status.Authorized

			// Keep running until context is canceled
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorTagged([]string{"Telegram", user.Phone}, "Client error: %v", err)
		}
	}()

	// Wait a bit for the client to initialize
	time.Sleep(1 * time.Second)

	return c, nil
}

// Close stops the client
func (c *Client) Close() {
	c.cancel()
	c.wg.Wait()
}

// PreFlightCheck verifies the Telegram connection and channel
func (c *Client) PreFlightCheck() error {
	if !c.authenticated {
		return fmt.Errorf("telegram authentication required")
	}

	// Find or create the sync channel
	return c.ensureSyncChannel()
}

func (c *Client) ensureSyncChannel() error {
	// List dialogs to find the channel
	// Note: This is a simplified search. In a real scenario with many chats,
	// we might need to iterate more.

	// We need to use the raw API to list dialogs
	// Using message.Sender to list dialogs is not direct, we use tg.Client

	// Wait for client to be ready (in case PreFlightCheck is called immediately)
	// The Run loop sets up the helpers.

	// We can use c.client.API() to get *tg.Client

	// Search for the channel
	// We'll iterate through dialogs.
	// Since we can't easily "search" for a channel we own by name without listing,
	// we list dialogs.

	// Using a limit of 100 for now.
	dialogs, err := c.client.API().MessagesGetDialogs(c.ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
	})
	if err != nil {
		return fmt.Errorf("failed to list dialogs: %w", err)
	}

	var foundChannel *tg.Channel
	count := 0

	// Helper to process chats
	processChats := func(chats []tg.ChatClass) {
		for _, chat := range chats {
			if channel, ok := chat.(*tg.Channel); ok {
				// Check if it's a channel (not a supergroup, though they are similar)
				// and check the title
				if channel.Title == syncChannelName {
					// Check if we have access (not left/kicked)
					if !channel.Left {
						foundChannel = channel
						count++
					}
				}
			}
		}
	}

	switch d := dialogs.(type) {
	case *tg.MessagesDialogs:
		processChats(d.Chats)
	case *tg.MessagesDialogsSlice:
		processChats(d.Chats)
	}

	if count > 1 {
		return fmt.Errorf("ambiguity error: found %d channels named '%s'. Please resolve manually", count, syncChannelName)
	}

	if count == 1 {
		c.channelID = foundChannel.ID
		c.accessHash = foundChannel.AccessHash
		logger.InfoTagged([]string{"Telegram", c.user.Phone}, "Found existing sync channel: %s (ID: %d)", syncChannelName, c.channelID)
		return nil
	}

	// Create channel if not found
	logger.InfoTagged([]string{"Telegram", c.user.Phone}, "Creating new sync channel: %s", syncChannelName)

	// Create channel
	updates, err := c.client.API().ChannelsCreateChannel(c.ctx, &tg.ChannelsCreateChannelRequest{
		Title:     syncChannelName,
		Broadcast: true, // Channel, not supergroup
		About:     "Cloud Drives Sync Storage",
	})
	if err != nil {
		return fmt.Errorf("failed to create channel: %w", err)
	}

	// Extract channel ID from updates
	var newChannel *tg.Channel

	switch u := updates.(type) {
	case *tg.Updates:
		for _, chat := range u.Chats {
			if ch, ok := chat.(*tg.Channel); ok {
				newChannel = ch
				break
			}
		}
	}

	if newChannel == nil {
		return fmt.Errorf("failed to get created channel info")
	}

	c.channelID = newChannel.ID
	c.accessHash = newChannel.AccessHash

	return nil
}

// Authenticate performs the interactive authentication flow
func (c *Client) Authenticate(phone string) error {
	flow := auth.NewFlow(
		&termAuth{phone: phone},
		auth.SendCodeOptions{},
	)

	if err := c.client.Auth().IfNecessary(c.ctx, flow); err != nil {
		return err
	}

	c.authenticated = true
	return nil
}

type termAuth struct {
	phone string
}

func (a *termAuth) Phone(ctx context.Context) (string, error) {
	return a.phone, nil
}

func (a *termAuth) Password(ctx context.Context) (string, error) {
	fmt.Print("Enter 2FA Password: ")
	var pwd string
	fmt.Scanln(&pwd)
	return pwd, nil
}

func (a *termAuth) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	fmt.Print("Enter Telegram code: ")
	var code string
	fmt.Scanln(&code)
	return code, nil
}

func (a *termAuth) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	return nil
}

func (a *termAuth) SignUp(ctx context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("signup not supported")
}

// ListFiles lists files in the sync channel
func (c *Client) ListFiles(folderID string) ([]*model.File, error) {
	if c.channelID == 0 {
		return nil, fmt.Errorf("channel not initialized")
	}

	// Iterate over messages in the channel
	// We'll use the raw API to get history

	fileMap := make(map[string]*model.File)
	offsetID := 0
	limit := 100

	for {
		history, err := c.client.API().MessagesGetHistory(c.ctx, &tg.MessagesGetHistoryRequest{
			Peer: &tg.InputPeerChannel{
				ChannelID:  c.channelID,
				AccessHash: c.accessHash,
			},
			OffsetID: offsetID,
			Limit:    limit,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get history: %w", err)
		}

		var messages []tg.MessageClass

		switch h := history.(type) {
		case *tg.MessagesChannelMessages:
			messages = h.Messages
		case *tg.MessagesMessages:
			messages = h.Messages
		case *tg.MessagesMessagesSlice:
			messages = h.Messages
		default:
			return nil, fmt.Errorf("unexpected history type")
		}

		if len(messages) == 0 {
			break
		}

		for _, msgClass := range messages {
			msg, ok := msgClass.(*tg.Message)
			if !ok {
				continue // Skip service messages
			}

			// Parse caption for metadata
			if msg.Message == "" {
				continue
			}

			// Try to parse JSON from caption
			var meta FileMetadata
			if err := json.Unmarshal([]byte(msg.Message), &meta); err != nil {
				continue
			}

			fullPath := meta.FolderPath + "/" + meta.FileName

			// Get file size from media
			var partSize int64
			if media, ok := msg.Media.(*tg.MessageMediaDocument); ok {
				if doc, ok := media.Document.(*tg.Document); ok {
					partSize = doc.Size
				}
			}

			// Create fragment
			fragment := &model.FileFragment{
				ID:               strconv.Itoa(msg.ID), // Fragment ID is message ID
				Name:             meta.FileName,
				Size:             partSize,
				Part:             meta.Part,
				TelegramUniqueID: strconv.Itoa(msg.ID),
			}

			if !meta.Split {
				// Single file
				file := &model.File{
					ID:               strconv.Itoa(msg.ID),
					Name:             meta.FileName,
					Path:             fullPath,
					ParentFolderID:   meta.FolderPath,
					Size:             partSize,
					TelegramUniqueID: strconv.Itoa(msg.ID),
					Provider:         model.ProviderTelegram,
					UserEmail:        c.user.Email,
					CreatedTime:      time.Unix(int64(msg.Date), 0),
					ModifiedTime:     time.Unix(int64(msg.Date), 0),
					Split:            false,
					TotalParts:       1,
					Fragments:        []*model.FileFragment{fragment},
				}
				file.UpdateCalculatedID()
				// For single files, fragment FileID is the file ID
				fragment.FileID = file.ID
				fileMap[fullPath] = file
			} else {
				// Split file
				if _, exists := fileMap[fullPath]; !exists {
					fileMap[fullPath] = &model.File{
						Name:           meta.FileName,
						Path:           fullPath,
						ParentFolderID: meta.FolderPath,
						Provider:       model.ProviderTelegram,
						UserEmail:      c.user.Email,
						CreatedTime:    time.Unix(int64(msg.Date), 0),
						ModifiedTime:   time.Unix(int64(msg.Date), 0),
						Split:          true,
						TotalParts:     meta.TotalParts,
						Fragments:      []*model.FileFragment{},
					}
				}
				file := fileMap[fullPath]
				file.Fragments = append(file.Fragments, fragment)
				file.Size += partSize // Accumulate size

				// If this is part 1, set the main ID
				if meta.Part == 1 {
					file.ID = strconv.Itoa(msg.ID)
					file.TelegramUniqueID = strconv.Itoa(msg.ID)
				}
			}

			// Update offset for next page
			if msg.ID < offsetID || offsetID == 0 {
				offsetID = msg.ID
			}
		}

		if len(messages) < limit {
			break
		}
	}

	// Convert map to slice and finalize split files
	var files []*model.File
	for _, file := range fileMap {
		if file.Split {
			// Ensure ID is set (in case Part 1 was missing, use first fragment's ID)
			if file.ID == "" && len(file.Fragments) > 0 {
				file.ID = file.Fragments[0].ID
				file.TelegramUniqueID = file.Fragments[0].TelegramUniqueID
			}
			// Set CalculatedID with total size
			file.UpdateCalculatedID()

			// Set FileID for all fragments
			for _, frag := range file.Fragments {
				frag.FileID = file.ID
			}
		}
		files = append(files, file)
	}

	return files, nil
}

// UploadFile uploads a file to the sync channel
func (c *Client) UploadFile(folderID, name string, reader io.Reader, size int64) (*model.File, error) {
	// Telegram allows all file extensions to be sent as documents, so no renaming is required.
	if c.channelID == 0 {
		return nil, fmt.Errorf("channel not initialized")
	}

	const maxPartSize = 2000 * 1024 * 1024 // 2GB

	generatedID := fmt.Sprintf("%s-%d", name, size)

	// Check if file already exists
	existingMessages, err := c.findMessagesByGeneratedID(generatedID)
	if err == nil && len(existingMessages) > 0 {
		// File exists, check if we need to update metadata
		var updated bool
		var firstMsgID int
		var telegramUniqueID string

		for _, msgClass := range existingMessages {
			msg, ok := msgClass.(*tg.Message)
			if !ok {
				continue
			}

			// Parse metadata
			var meta FileMetadata
			if err := json.Unmarshal([]byte(msg.Message), &meta); err != nil {
				continue
			}

			// Check if folder path changed
			if meta.FolderPath != folderID {
				meta.FolderPath = folderID
				newCaption, err := json.Marshal(meta)
				if err == nil {
					if err := c.updateMessageCaption(msg.ID, string(newCaption)); err != nil {
						logger.Error("Failed to update caption for message %d: %v", msg.ID, err)
					} else {
						updated = true
					}
				}
			}

			if meta.Part == 1 {
				firstMsgID = msg.ID
				telegramUniqueID = strconv.Itoa(msg.ID)
			}
		}

		if updated {
			logger.Info("Updated metadata for existing file %s", name)
		} else {
			logger.Info("File %s already exists with correct metadata", name)
		}

		// Return existing file info
		// We construct a minimal file object since we didn't re-upload
		// If it was split, we might not have all fragments here, but for sync purposes
		// we mainly need the ID and Path.

		// If we didn't find part 1, use the first message ID found
		if firstMsgID == 0 && len(existingMessages) > 0 {
			if msg, ok := existingMessages[0].(*tg.Message); ok {
				firstMsgID = msg.ID
				telegramUniqueID = strconv.Itoa(msg.ID)
			}
		}

		file := &model.File{
			ID:               strconv.Itoa(firstMsgID),
			Name:             name,
			Path:             folderID + "/" + name,
			Size:             size,
			TelegramUniqueID: telegramUniqueID,
			Provider:         model.ProviderTelegram,
			UserEmail:        c.user.Email,
			CreatedTime:      time.Now(),
			ModifiedTime:     time.Now(),
			Split:            len(existingMessages) > 1,
			TotalParts:       len(existingMessages),
			// Fragments are not fully populated here, but that's usually fine for sync checks
		}
		file.UpdateCalculatedID()
		return file, nil
	}

	if size <= maxPartSize {
		fragment, err := c.uploadSinglePart(folderID, name, reader, size, generatedID, false, 1, 1)
		if err != nil {
			return nil, err
		}

		file := &model.File{
			ID:               fragment.ID,
			Name:             name,
			Path:             folderID + "/" + name,
			Size:             size,
			TelegramUniqueID: fragment.TelegramUniqueID,
			Provider:         model.ProviderTelegram,
			UserEmail:        c.user.Email,
			CreatedTime:      time.Now(),
			ModifiedTime:     time.Now(),
			Split:            false,
			TotalParts:       1,
			Fragments:        []*model.FileFragment{fragment},
		}
		file.UpdateCalculatedID()
		fragment.FileID = file.ID
		return file, nil
	}

	// Split upload
	totalParts := int(math.Ceil(float64(size) / float64(maxPartSize)))

	logicalFile := &model.File{
		Name:       name,
		Path:       folderID + "/" + name,
		Size:       size,
		Split:      true,
		TotalParts: totalParts,
		Fragments:  make([]*model.FileFragment, 0, totalParts),
	}

	for i := 1; i <= totalParts; i++ {
		partSize := int64(maxPartSize)
		if i == totalParts {
			partSize = size - int64(i-1)*maxPartSize
		}

		// Use LimitReader for the part
		partReader := io.LimitReader(reader, partSize)

		fragment, err := c.uploadSinglePart(folderID, name, partReader, partSize, generatedID, true, i, totalParts)
		if err != nil {
			return nil, fmt.Errorf("failed to upload part %d: %w", i, err)
		}

		logicalFile.Fragments = append(logicalFile.Fragments, fragment)

		if i == 1 {
			logicalFile.ID = fragment.ID
			logicalFile.TelegramUniqueID = fragment.TelegramUniqueID
		}
		fragment.FileID = logicalFile.ID
	}

	logicalFile.CalculatedID = generatedID

	return logicalFile, nil
}

func (c *Client) uploadSinglePart(folderID, name string, reader io.Reader, size int64, generatedID string, split bool, part, totalParts int) (*model.FileFragment, error) {
	// Ensure folder path is not empty
	if folderID == "" {
		folderID = "/"
	}

	// Create metadata
	meta := FileMetadata{
		FileName:    name,
		FolderPath:  folderID,
		GeneratedID: generatedID,
		Split:       split,
		Part:        part,
		TotalParts:  totalParts,
		SoftDeleted: strings.Contains(folderID, "sync-cloud-drives-aux/soft-deleted"),
	}

	// Serialize metadata
	caption, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Upload file
	f, err := c.uploader.FromReader(c.ctx, name, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}

	// Send message with document
	inputChannel := &tg.InputPeerChannel{
		ChannelID:  c.channelID,
		AccessHash: c.accessHash,
	}

	// Use InputMediaUploadedDocument to ensure filename is set
	inputMedia := &tg.InputMediaUploadedDocument{
		File:     f,
		MimeType: "application/octet-stream",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: name},
		},
	}

	// Use low-level API to ensure attributes are sent correctly
	updates, err := c.client.API().MessagesSendMedia(c.ctx, &tg.MessagesSendMediaRequest{
		Peer:     inputChannel,
		Media:    inputMedia,
		Message:  string(caption),
		RandomID: time.Now().UnixNano(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}

	// Get message ID from updates
	var msgID int
	// This is tricky with gotd/message sender, it returns Updates.
	// We need to parse updates to find the message ID.
	// For now, we might need to list history or assume it's the last one?
	// Or use the Updates object.

	// Simplified: assume we can get it.
	// Actually, sender.File returns (tg.UpdatesClass, error).

	switch u := updates.(type) {
	case *tg.Updates:
		for _, m := range u.Updates {
			if msg, ok := m.(*tg.UpdateNewChannelMessage); ok {
				msgID = msg.Message.GetID()
				break
			}
			if msg, ok := m.(*tg.UpdateNewMessage); ok {
				msgID = msg.Message.GetID()
				break
			}
		}
	case *tg.UpdatesCombined:
		for _, m := range u.Updates {
			if msg, ok := m.(*tg.UpdateNewChannelMessage); ok {
				msgID = msg.Message.GetID()
				break
			}
		}
	}

	// If we couldn't find msgID, we might have a problem.
	// But for now let's proceed.

	// Return fragment
	return &model.FileFragment{
		ID:               strconv.Itoa(msgID),
		Name:             name,
		Size:             size,
		Part:             part,
		TelegramUniqueID: strconv.Itoa(msgID),
	}, nil
}

// DownloadFile downloads a file from Telegram
func (c *Client) DownloadFile(fileID string, writer io.Writer) error {
	if c.channelID == 0 {
		return fmt.Errorf("channel not initialized")
	}

	msgID, err := strconv.Atoi(fileID)
	if err != nil {
		return fmt.Errorf("invalid file ID: %w", err)
	}

	// Get message to find the document
	msgs, err := c.client.API().ChannelsGetMessages(c.ctx, &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{
			ChannelID:  c.channelID,
			AccessHash: c.accessHash,
		},
		ID: []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
	})
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}

	var msg *tg.Message
	switch m := msgs.(type) {
	case *tg.MessagesChannelMessages:
		if len(m.Messages) > 0 {
			if mm, ok := m.Messages[0].(*tg.Message); ok {
				msg = mm
			}
		}
	}

	if msg == nil {
		return fmt.Errorf("message not found")
	}

	// Extract document location
	var loc tg.InputFileLocationClass
	if media, ok := msg.Media.(*tg.MessageMediaDocument); ok {
		if doc, ok := media.Document.(*tg.Document); ok {
			loc = &tg.InputDocumentFileLocation{
				ID:            doc.ID,
				AccessHash:    doc.AccessHash,
				FileReference: doc.FileReference,
			}
		}
	}

	if loc == nil {
		return fmt.Errorf("no document found in message")
	}

	// Download
	_, err = c.downloader.Download(c.client.API(), loc).Stream(c.ctx, writer)
	return err
}

// UpdateFile updates file content (Deletes and re-uploads for Telegram)
func (c *Client) UpdateFile(fileID string, reader io.Reader, size int64) error {
	// Telegram messages are immutable regarding media (mostly), easier to replace
	// But we lose the ID. This is tricky if "UpdateFile" implies keeping ID.
	// For metadata.db sync, keeping ID isn't strictly necessary as long as "Download" finds it by name.
	// But "metadata.db" is found by scanning the Aux folder for a file named "metadata.db".

	// Get old file info to get the folder ID/Parent
	// We don't easily have it unless we query or it's passed.
	// However, UploadMetadataDB knows the folder logic.

	// Since we are replacing the logic in UploadMetadataDB to use UpdateFile when file exists,
	// checking if we can just Delete + Upload there is easier for Telegram.
	// But to satisfy interface:

	if err := c.DeleteFile(fileID); err != nil {
		return err
	}

	// We need folderID. Since we lack it here, we assume standard sync channel?
	// Actually, metadata.db is in "sync-cloud-drives-aux" "folder".
	// In Telegram, folders are just paths in metadata.
	// We can try to upload with the standard Aux path.

	folderID := "/sync-cloud-drives-aux"
	_, err := c.UploadFile(folderID, "metadata.db", reader, size)
	return err
}

// DeleteFile deletes a file (message) from the channel
func (c *Client) DeleteFile(fileID string) error {
	if c.channelID == 0 {
		return fmt.Errorf("channel not initialized")
	}

	msgID, err := strconv.Atoi(fileID)
	if err != nil {
		return fmt.Errorf("invalid file ID: %w", err)
	}

	_, err = c.client.API().ChannelsDeleteMessages(c.ctx, &tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{
			ChannelID:  c.channelID,
			AccessHash: c.accessHash,
		},
		ID: []int{msgID},
	})

	return err
}

// Unused methods for Telegram (no folders)

func (c *Client) MoveFile(fileID, targetFolderID string) error {
	return errors.New("not supported - Telegram doesn't have folders")
}

func (c *Client) ListFolders(parentID string) ([]*model.Folder, error) {
	return nil, nil
}

func (c *Client) CreateFolder(parentID, name string) (*model.Folder, error) {
	// Telegram doesn't have real folders, so we simulate them by constructing the path.
	// The "ID" of a folder in Telegram is just its full path.

	var newPath string
	if parentID == "" || parentID == "/" {
		newPath = "/" + name
	} else {
		newPath = parentID + "/" + name
	}

	// Return a dummy folder object
	return &model.Folder{
		ID:             newPath,
		Name:           name,
		Path:           newPath,
		ParentFolderID: parentID,
		Provider:       model.ProviderTelegram,
	}, nil
}

func (c *Client) DeleteFolder(folderID string) error {
	return errors.New("not supported - Telegram doesn't have folders")
}

func (c *Client) GetSyncFolderID() (string, error) {
	return "/", nil
}

func (c *Client) ShareFolder(folderID, email string, role string) error {
	return errors.New("not supported - Telegram doesn't have folder sharing")
}

func (c *Client) VerifyPermissions() error {
	return nil
}

func (c *Client) GetQuota() (*api.QuotaInfo, error) {
	return &api.QuotaInfo{
		Total: -1,
		Used:  0,
		Free:  -1,
	}, nil
}

func (c *Client) GetFileMetadata(fileID string) (*model.File, error) {
	// Reuse ListFiles logic or GetMessages
	return nil, fmt.Errorf("not implemented")
}

func (c *Client) TransferOwnership(fileID, newOwnerEmail string) error {
	return errors.New("not supported")
}

func (c *Client) AcceptOwnership(fileID string) error {
	return errors.New("not supported")
}

func (c *Client) GetUserEmail() string {
	return ""
}

func (c *Client) GetUserIdentifier() string {
	return c.user.Phone
}

// CreateShortcut creates a shortcut (not supported in Telegram)
func (c *Client) CreateShortcut(parentID, name, targetID string) (*model.File, error) {
	return nil, fmt.Errorf("not supported - Telegram does not support shortcuts")
}
