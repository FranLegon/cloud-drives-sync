package database

import (
	"os"
	"testing"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

func TestUpdateLogicalFilesFromReplicasPrefersRealReplicaOverPlaceholder(t *testing.T) {
	password := "placeholderPass!23"
	path := GetDBPath()
	os.Remove(path)
	if err := CreateDB(password); err != nil {
		t.Fatalf("CreateDB: %v", err)
	}
	defer os.Remove(path)

	db, err := Open(password)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	f := &model.File{
		ID:             "file-1",
		Path:           "/docs/report.pdf",
		Name:           "report.pdf",
		Size:           123,
		CalculatedID:   "report.pdf-123",
		GoogleDriveMD5: "d41d8cd98f00b204e9800998ecf8427e",
		ModTime:        time.Unix(1000, 0),
		Status:         "active",
		Replicas: []*model.Replica{
			{
				FileID:       "file-1",
				CalculatedID: "report.pdf-123",
				Path:         "/docs/report.pdf",
				Name:         "report.pdf",
				Size:         123,
				Provider:     model.ProviderGoogle,
				AccountID:    "backup@gmail.com",
				NativeID:     "gid-1",
				NativeHash:   "d41d8cd98f00b204e9800998ecf8427e",
				ModTime:      time.Unix(1000, 0),
				Status:       "active",
				Owner:        "backup@gmail.com",
			},
			{
				FileID:       "file-1",
				CalculatedID: "d41d8cd98f00b204e9800998ecf8427e",
				Path:         "/docs/report.pdf",
				Name:         "report.pdf",
				Size:         0,
				Provider:     model.ProviderMicrosoft,
				AccountID:    "backup@company.com",
				NativeID:     "ms-1",
				NativeHash:   model.NativeHashShortcut,
				ModTime:      time.Unix(2000, 0),
				Status:       "active",
				Owner:        "SHARED",
			},
		},
	}
	if err := db.InsertFile(f); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	if err := db.UpdateLogicalFilesFromReplicas(); err != nil {
		t.Fatalf("UpdateLogicalFilesFromReplicas: %v", err)
	}

	got, err := db.GetFileByID("file-1")
	if err != nil {
		t.Fatalf("GetFileByID: %v", err)
	}
	if got.Size != 123 {
		t.Fatalf("logical size = %d, want 123", got.Size)
	}
	if got.Name != "report.pdf" {
		t.Fatalf("logical name = %q, want report.pdf", got.Name)
	}
	if got.Path != "/docs/report.pdf" {
		t.Fatalf("logical path = %q, want /docs/report.pdf", got.Path)
	}
	if got.GoogleDriveMD5 != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Fatalf("logical GoogleDriveMD5 = %q", got.GoogleDriveMD5)
	}
}
