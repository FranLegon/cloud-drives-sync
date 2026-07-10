package task

import (
	"os"
	"testing"

	"github.com/FranLegon/cloud-drives-sync/internal/database"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

func TestPreloadFolderCacheUsesFolderReplicas(t *testing.T) {
	password := "folderCachePass!23"
	path := database.GetDBPath()
	os.Remove(path)
	if err := database.CreateDB(password); err != nil {
		t.Fatalf("CreateDB: %v", err)
	}
	defer os.Remove(path)

	db, err := database.Open(password)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if err := db.InsertLogicalFolder(&model.LogicalFolder{ID: "lf-1", Path: "/nested/path", Name: "path", ParentLogicalFolderID: "lf-nested", Status: "active"}); err != nil {
		t.Fatalf("InsertLogicalFolder: %v", err)
	}
	if err := db.InsertFolderReplica(&model.FolderReplica{LogicalFolderID: "lf-1", Provider: model.ProviderGoogle, AccountID: "main@x.com", NativeFolderID: "gid-123", Owner: "main@x.com", LastSeenAt: 1}); err != nil {
		t.Fatalf("InsertFolderReplica: %v", err)
	}

	r := NewRunner(&model.Config{}, db, false)
	cacheKey := model.GenerateCacheKey(model.ProviderGoogle, "main@x.com") + ":nested/path"
	cachedID, ok := r.folderCache.Load(cacheKey)
	if !ok {
		t.Fatalf("expected cache entry for %s", cacheKey)
	}
	if cachedID.(string) != "gid-123" {
		t.Fatalf("expected cached folder id gid-123, got %v", cachedID)
	}
}
