package model

import (
	"strings"
	"time"
)

// NormalizePath ensures standard forward-slash separators
func NormalizePath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// GenerateCacheKey creates a consistent cache string for provider and accountID
func GenerateCacheKey(provider Provider, accountID string) string {
	return string(provider) + ":" + accountID
}

// CacheKey returns a consistent key for caching per user
func (u *User) CacheKey() string {
	return GenerateCacheKey(u.Provider, u.GetAccountID())
}

// LogTags returns tags for logger
func (u *User) LogTags() []string {
	return []string{string(u.Provider), u.GetAccountID()}
}

// LogTags returns tags for logger
func (r *Replica) LogTags() []string {
	return []string{string(r.Provider), r.AccountID}
}

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

// GetAccountID returns the primary identifier for the user (Phone for Telegram, Email otherwise)
func (u *User) GetAccountID() string {
	if u.Provider == ProviderTelegram {
		return u.Phone
	}
	return u.Email
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
	Provider       Provider
	Total          int64
	Used           int64
	Free           int64
	SyncFolderUsed int64
}

// File represents a logical file
type File struct {
	ID             string     // Internal UUID
	Path           string     // Logical relative path
	Name           string     // Filename
	Size           int64      // File size in bytes
	GoogleDriveMD5 string     // Canonical cross-provider identity (SPEC): Google Drive MD5
	ModTime        time.Time  // Modification timestamp
	Status         string     // active, soft-deleted, deleted
	Replicas       []*Replica // Physical copies
}

// Replica represents a physical copy of a file on a cloud provider
type Replica struct {
	ID         int64              `json:"id"`
	FileID     string             `json:"file_id"`
	Path       string             `json:"path"`
	Name       string             `json:"name"`
	Size       int64              `json:"size"`
	Provider   Provider           `json:"provider"`
	AccountID  string             `json:"account_id"`
	NativeID   string             `json:"native_id"`
	NativeHash string             `json:"native_hash"`
	ModTime    time.Time          `json:"mod_time"`
	Status     string             `json:"status"`
	Fragmented bool               `json:"fragmented"`
	Owner      string             `json:"owner"`
	Fragments  []*ReplicaFragment `json:"-"`
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

// LogicalFolder represents a provider-agnostic folder (SPEC new-model).
type LogicalFolder struct {
	ID                    string // Internal UUID
	Path                  string // Logical relative path
	Name                  string // Folder name
	ParentLogicalFolderID string // Parent logical_folder ID (empty for top-level)
	Status                string // active, soft-deleted, deleted
}

// FolderReplica represents one physical copy of a logical_folder on a specific account.
// Google: one replica owned by main and shared. OneDrive: one per backup account. Telegram: none.
type FolderReplica struct {
	ID              int64
	LogicalFolderID string
	Provider        Provider
	AccountID       string
	NativeFolderID  string // provider's stable folder ID
	Owner           string // owner account
	LastSeenAt      int64  // last time confirmed to still exist (unix)
}

// SyncRun represents a tracked sync pipeline execution for crash recovery
type SyncRun struct {
	ID                int64
	StartedAt         time.Time
	CompletedAt       *time.Time
	LastCompletedStep int
	SafeMode          bool
}
