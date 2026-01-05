package model

import "time"

// Provider represents a cloud storage provider
type Provider string

const (
	ProviderGoogle    Provider = "Google"
	ProviderMicrosoft Provider = "Microsoft"
	ProviderTelegram  Provider = "Telegram"
)

// GoogleClient represents Google API client credentials
type GoogleClient struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// MicrosoftClient represents Microsoft API client credentials
type MicrosoftClient struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// TelegramClient represents Telegram API client credentials
type TelegramClient struct {
	APIID   string `json:"api_id"`
	APIHash string `json:"api_hash"`
	Phone   string `json:"phone"`
}

// User represents a user account for a provider
type User struct {
	Provider       Provider `json:"provider"`
	Email          string   `json:"email,omitempty"`
	Phone          string   `json:"phone,omitempty"`
	IsMain         bool     `json:"is_main"`
	RefreshToken   string   `json:"refresh_token,omitempty"`
	SessionData    string   `json:"session_data,omitempty"`
	SyncFolderName string   `json:"sync_folder_name,omitempty"`
}

// Config represents the application configuration
type Config struct {
	GoogleClient    GoogleClient    `json:"google_client"`
	MicrosoftClient MicrosoftClient `json:"microsoft_client"`
	TelegramClient  TelegramClient  `json:"telegram_client"`
	Users           []User          `json:"users"`
}

// ProviderQuota represents aggregated quota for a provider
type ProviderQuota struct {
	Provider Provider
	Total    int64
	Used     int64
	Free     int64
}

// File represents a file in cloud storage
type File struct {
	ID                   string
	Name                 string
	Path                 string
	Size                 int64
	GoogleDriveHash      string
	GoogleDriveID        string
	OneDriveHash         string
	OneDriveID           string
	TelegramUniqueID     string
	CalculatedSHA256Hash string
	CalculatedID         string
	Provider             Provider
	UserEmail            string
	UserPhone            string
	CreatedTime          time.Time
	ModifiedTime         time.Time
	OwnerEmail           string
	ParentFolderID       string
	Split                bool
	TotalParts           int
	Fragments            []*FileFragment
}

// FileFragment represents a part of a split file
type FileFragment struct {
	ID               string
	FileID           string
	Name             string
	Size             int64
	Part             int
	TelegramUniqueID string
}

// Folder represents a folder in cloud storage
type Folder struct {
	ID             string
	Name           string
	Path           string
	Provider       Provider
	UserEmail      string
	UserPhone      string
	ParentFolderID string
	OwnerEmail     string
}
