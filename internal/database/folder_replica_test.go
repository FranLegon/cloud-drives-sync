package database

import (
	"os"
	"testing"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

// TestFolderUpsertIdempotence verifies that re-inserting identical logical_folder and
// folder_replica rows (with only last_seen_at changing) does NOT bump the metadata version,
// preserving sync idempotence (SPEC test case 19).
func TestFolderUpsertIdempotence(t *testing.T) {
	password := "folderIdemPass!23"
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

	lf := &model.LogicalFolder{ID: "lf-aux", Path: "/cloud-drives-sync-aux", Name: "cloud-drives-sync-aux", Status: "active"}
	fr := &model.FolderReplica{LogicalFolderID: "lf-aux", Provider: model.ProviderGoogle, AccountID: "main@x.com", NativeFolderID: "gid-1", Owner: "main@x.com", LastSeenAt: 1000}

	if err := db.InsertLogicalFolder(lf); err != nil {
		t.Fatalf("InsertLogicalFolder: %v", err)
	}
	if err := db.InsertFolderReplica(fr); err != nil {
		t.Fatalf("InsertFolderReplica: %v", err)
	}

	hashAfterFirst, err := db.GetMetadataHash()
	if err != nil {
		t.Fatalf("GetMetadataHash: %v", err)
	}

	// Re-insert identical rows, only advancing last_seen_at.
	fr.LastSeenAt = 2000
	if err := db.InsertLogicalFolder(lf); err != nil {
		t.Fatalf("InsertLogicalFolder (2): %v", err)
	}
	if err := db.InsertFolderReplica(fr); err != nil {
		t.Fatalf("InsertFolderReplica (2): %v", err)
	}

	hashAfterSecond, err := db.GetMetadataHash()
	if err != nil {
		t.Fatalf("GetMetadataHash (2): %v", err)
	}

	if hashAfterFirst != hashAfterSecond {
		t.Fatalf("metadata version changed on idempotent re-insert: %s -> %s", hashAfterFirst, hashAfterSecond)
	}

	// A real change (owner) MUST bump the version.
	fr.Owner = "other@x.com"
	if err := db.InsertFolderReplica(fr); err != nil {
		t.Fatalf("InsertFolderReplica (3): %v", err)
	}
	hashAfterChange, err := db.GetMetadataHash()
	if err != nil {
		t.Fatalf("GetMetadataHash (3): %v", err)
	}
	if hashAfterChange == hashAfterSecond {
		t.Fatalf("metadata version did not change after a real folder_replica change")
	}
}
