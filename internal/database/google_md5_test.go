package database

import (
	"os"
	"testing"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

// TestUpdateLogicalFilesGoogleMD5 verifies the canonical Google Drive MD5 is copied from the active
// Google replica onto the logical file, and that re-running is idempotent (no version bump).
func TestUpdateLogicalFilesGoogleMD5(t *testing.T) {
	password := "gmd5Pass!23"
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
		ID: "file-1", Path: "/a.txt", Name: "a.txt", Size: 3,
		CalculatedID: "a.txt-3", ModTime: time.Unix(1000, 0), Status: "active",
		Replicas: []*model.Replica{{
			FileID: "file-1", CalculatedID: "a.txt-3", Path: "/a.txt", Name: "a.txt", Size: 3,
			Provider: model.ProviderGoogle, AccountID: "b@x.com", NativeID: "gid1",
			NativeHash: "md5abc", ModTime: time.Unix(1000, 0), Status: "active", Owner: "b@x.com",
		}},
	}
	if err := db.InsertFile(f); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	if err := db.UpdateLogicalFilesGoogleMD5(); err != nil {
		t.Fatalf("UpdateLogicalFilesGoogleMD5: %v", err)
	}

	var got string
	if err := db.queryRow("SELECT google_drive_md5 FROM files WHERE id = ?", "file-1").Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != "md5abc" {
		t.Fatalf("google_drive_md5 = %q, want %q", got, "md5abc")
	}

	h1, err := db.GetMetadataHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := db.UpdateLogicalFilesGoogleMD5(); err != nil {
		t.Fatalf("UpdateLogicalFilesGoogleMD5 (2): %v", err)
	}
	h2, err := db.GetMetadataHash()
	if err != nil {
		t.Fatalf("hash2: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("second identical run bumped version: %s -> %s", h1, h2)
	}
}
