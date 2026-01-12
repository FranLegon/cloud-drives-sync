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

	NativeHashShortcut = "SHORTCUT"
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
	ID           string     // Internal UUID
	Path         string     // Logical relative path
	Name         string     // Filename
	Size         int64      // File size in bytes
	CalculatedID string     // CONCAT(name, '-', size) for deduplication
	ModTime      time.Time  // Modification timestamp
	Status       string     // active, softdeleted, deleted
	Replicas     []*Replica // Physical copies
}

// Replica represents a physical copy of a file on a cloud provider
type Replica struct {
	ID           int64              `json:"id"`
	FileID       string             `json:"file_id"`
	CalculatedID string             `json:"calculated_id"`
	Path         string             `json:"path"`
	Name         string             `json:"name"`
	Size         int64              `json:"size"`
	Provider     Provider           `json:"provider"`
	AccountID    string             `json:"account_id"`
	NativeID     string             `json:"native_id"`
	NativeHash   string             `json:"native_hash"`
	ModTime      time.Time          `json:"mod_time"`
	Status       string             `json:"status"`
	Fragmented   bool               `json:"fragmented"`
	Fragments    []*ReplicaFragment `json:"-"`
}

// ReplicaFragment represents a part of a split file (Telegram)
type ReplicaFragment struct {
	ID               int64  `json:"id"`
	ReplicaID        int64  `json:"replica_id"`
	FragmentNumber   int    `json:"fragment_number"`
	FragmentsTotal   int    `json:"fragments_total"`
	Size             int64  `json:"size"`
	NativeFragmentID string `json:"native_fragment_id"`
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
