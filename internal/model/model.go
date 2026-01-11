package model

import (
	"time"
)

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
}

// User represents a user account for a provider
type User struct {
	Provider     Provider `json:"provider"`
	Email        string   `json:"email,omitempty"`
	Phone        string   `json:"phone,omitempty"`
	IsMain       bool     `json:"is_main"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	SessionData  string   `json:"session_data,omitempty"`
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

// File represents a logical file
type File struct {
	ID           string             // Internal UUID
	Path         string             // Logical relative path
	Name         string             // Filename
	Size         int64              // File size in bytes
	CalculatedID string             // CONCAT(name, '-', size) for deduplication
	ModTime      time.Time          // Modification timestamp
	Status       string             // active, softdeleted, deleted
	Replicas     []*Replica         // Physical copies
}

// Replica represents a physical copy of a file on a cloud provider
type Replica struct {
	ID           int64
	FileID       string    // References File.ID (nullable initially)
	CalculatedID string    // CONCAT(name, '-', size) for matching
	Path         string    // Logical relative path
	Name         string    // Filename
	Size         int64     // File size in bytes
	Provider     Provider  // google, onedrive, telegram
	AccountID    string    // Email or Phone
	NativeID     string    // Cloud Provider's stable ID
	NativeHash   string    // Cloud Provider Hash (MD5, SHA1, or null)
	ModTime      time.Time // Modification timestamp
	Status       string    // active, softdeleted, deleted
	Fragmented   bool      // true for Telegram files split into parts
}

// ReplicaFragment represents a part of a split file (Telegram)
type ReplicaFragment struct {
	ID               int64
	ReplicaID        int64
	FragmentNumber   int    // 1-based index
	FragmentsTotal   int    // Total number of fragments
	Size             int64  // Fragment size in bytes
	NativeFragmentID string // Telegram file_unique_id for the part
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
