package database

import (
	"os"
	"testing"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

func TestGetAllFolderReplicaViews(t *testing.T) {
	password := "folderViewPass!23"
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

	if err := db.InsertLogicalFolder(&model.LogicalFolder{ID: "lf-1", Path: "/a/b", Name: "b", ParentLogicalFolderID: "lf-a", Status: "active"}); err != nil {
		t.Fatalf("InsertLogicalFolder google: %v", err)
	}
	if err := db.InsertFolderReplica(&model.FolderReplica{LogicalFolderID: "lf-1", Provider: model.ProviderGoogle, AccountID: "main@x.com", NativeFolderID: "gid-1", Owner: "main@x.com", LastSeenAt: 1}); err != nil {
		t.Fatalf("InsertFolderReplica google: %v", err)
	}
	if err := db.InsertLogicalFolder(&model.LogicalFolder{ID: "lf-2", Path: "/tg/path", Name: "path", Status: "active"}); err != nil {
		t.Fatalf("InsertLogicalFolder telegram: %v", err)
	}
	if err := db.InsertFolderReplica(&model.FolderReplica{LogicalFolderID: "lf-2", Provider: model.ProviderTelegram, AccountID: "+34123456789", NativeFolderID: "tg-folder", Owner: "+34123456789", LastSeenAt: 2}); err != nil {
		t.Fatalf("InsertFolderReplica telegram: %v", err)
	}

	views, err := db.GetAllFolderReplicaViews()
	if err != nil {
		t.Fatalf("GetAllFolderReplicaViews: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("expected 2 folder views, got %d", len(views))
	}

	var googleView, telegramView *model.Folder
	for _, v := range views {
		switch v.Provider {
		case model.ProviderGoogle:
			googleView = v
		case model.ProviderTelegram:
			telegramView = v
		}
	}

	if googleView == nil || googleView.ID != "gid-1" || googleView.UserEmail != "main@x.com" || googleView.Path != "/a/b" || googleView.ParentFolderID != "lf-a" {
		t.Fatalf("unexpected google folder view: %+v", googleView)
	}
	if telegramView == nil || telegramView.ID != "tg-folder" || telegramView.UserPhone != "+34123456789" || telegramView.UserEmail != "" || telegramView.Path != "/tg/path" {
		t.Fatalf("unexpected telegram folder view: %+v", telegramView)
	}
}
