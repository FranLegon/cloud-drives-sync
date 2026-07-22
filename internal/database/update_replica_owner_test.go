package database

import (
	"os"
	"testing"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

func TestUpdateReplicaOwnerUpdatesAccountAndOwner(t *testing.T) {
	password := "updateReplicaOwnerPass!23"
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

	file := &model.File{
		ID:      "file-update-owner",
		Path:    "/docs/report.txt",
		Name:    "report.txt",
		Size:    42,
		ModTime: time.Unix(1000, 0),
		Status:  "active",
		Replicas: []*model.Replica{
			{
				FileID:     "file-update-owner",
				Path:       "/docs/report.txt",
				Name:       "report.txt",
				Size:       42,
				Provider:   model.ProviderGoogle,
				AccountID:  "source@gmail.com",
				NativeID:   "native-1",
				NativeHash: "hash-1",
				ModTime:    time.Unix(1000, 0),
				Status:     "active",
				Owner:      "source@gmail.com",
			},
		},
	}
	if err := db.InsertFile(file); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	if err := db.UpdateReplicaOwner(string(model.ProviderGoogle), "source@gmail.com", "native-1", "target@gmail.com"); err != nil {
		t.Fatalf("UpdateReplicaOwner: %v", err)
	}

	replicas, err := db.GetReplicas("file-update-owner")
	if err != nil {
		t.Fatalf("GetReplicas: %v", err)
	}
	if len(replicas) != 1 {
		t.Fatalf("replica count = %d, want 1", len(replicas))
	}
	if replicas[0].AccountID != "target@gmail.com" {
		t.Fatalf("account_id = %q, want target@gmail.com", replicas[0].AccountID)
	}
	if replicas[0].Owner != "target@gmail.com" {
		t.Fatalf("owner = %q, want target@gmail.com", replicas[0].Owner)
	}
}

func TestUpdateReplicaOwnerIsIdempotentWhenAlreadyMoved(t *testing.T) {
	password := "updateReplicaOwnerIdempotentPass!23"
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

	file := &model.File{
		ID:      "file-already-moved",
		Path:    "/docs/already.txt",
		Name:    "already.txt",
		Size:    7,
		ModTime: time.Unix(2000, 0),
		Status:  "active",
		Replicas: []*model.Replica{
			{
				FileID:     "file-already-moved",
				Path:       "/docs/already.txt",
				Name:       "already.txt",
				Size:       7,
				Provider:   model.ProviderGoogle,
				AccountID:  "target@gmail.com",
				NativeID:   "native-2",
				NativeHash: "hash-2",
				ModTime:    time.Unix(2000, 0),
				Status:     "active",
				Owner:      "target@gmail.com",
			},
		},
	}
	if err := db.InsertFile(file); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	if err := db.UpdateReplicaOwner(string(model.ProviderGoogle), "source@gmail.com", "native-2", "target@gmail.com"); err != nil {
		t.Fatalf("UpdateReplicaOwner: %v", err)
	}

	replicas, err := db.GetReplicas("file-already-moved")
	if err != nil {
		t.Fatalf("GetReplicas: %v", err)
	}
	if len(replicas) != 1 {
		t.Fatalf("replica count = %d, want 1", len(replicas))
	}
	if replicas[0].AccountID != "target@gmail.com" || replicas[0].Owner != "target@gmail.com" {
		t.Fatalf("replica changed unexpectedly: account_id=%q owner=%q", replicas[0].AccountID, replicas[0].Owner)
	}
}
