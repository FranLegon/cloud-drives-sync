package model

import (
	"testing"
)

func TestProviderConstants(t *testing.T) {
	if ProviderGoogle != "Google" {
		t.Errorf("Expected ProviderGoogle to be 'Google', got %s", ProviderGoogle)
	}
	if ProviderMicrosoft != "Microsoft" {
		t.Errorf("Expected ProviderMicrosoft to be 'Microsoft', got %s", ProviderMicrosoft)
	}
	if ProviderTelegram != "Telegram" {
		t.Errorf("Expected ProviderTelegram to be 'Telegram', got %s", ProviderTelegram)
	}
}

func TestUserModel(t *testing.T) {
	user := User{
		Provider:     ProviderGoogle,
		Email:        "test@example.com",
		IsMain:       true,
		RefreshToken: "test-token",
	}

	if user.Provider != ProviderGoogle {
		t.Error("Provider not set correctly")
	}
	if user.Email != "test@example.com" {
		t.Error("Email not set correctly")
	}
	if !user.IsMain {
		t.Error("IsMain not set correctly")
	}
}

func TestConfigModel(t *testing.T) {
	config := Config{
		GoogleClient: GoogleClient{
			ID:     "test-id",
			Secret: "test-secret",
		},
		Users: []User{
			{
				Provider: ProviderGoogle,
				Email:    "user1@example.com",
				IsMain:   true,
			},
			{
				Provider: ProviderGoogle,
				Email:    "user2@example.com",
				IsMain:   false,
			},
		},
	}

	if len(config.Users) != 2 {
		t.Errorf("Expected 2 users, got %d", len(config.Users))
	}
	if config.GoogleClient.ID != "test-id" {
		t.Error("GoogleClient ID not set correctly")
	}
}

func TestFileModel(t *testing.T) {
	file := File{
		ID:           "file-123",
		Name:         "test.txt",
		Size:         1024,
		CalculatedID: "test.txt-1024",
	}

	if file.ID != "file-123" {
		t.Error("File ID not set correctly")
	}
	if file.Size != 1024 {
		t.Error("File size not set correctly")
	}
	if file.CalculatedID != "test.txt-1024" {
		t.Error("File CalculatedID not set correctly")
	}
}

func TestFolderModel(t *testing.T) {
	folder := Folder{
		ID:        "folder-456",
		Name:      "Documents",
		Provider:  ProviderGoogle,
		UserEmail: "test@example.com",
	}

	if folder.ID != "folder-456" {
		t.Error("Folder ID not set correctly")
	}
	if folder.Name != "Documents" {
		t.Error("Folder name not set correctly")
	}
}
