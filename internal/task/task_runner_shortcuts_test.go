package task

import (
	"testing"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

func TestHasActiveMicrosoftReplicaAtPath(t *testing.T) {
	file := &model.File{
		Path: "/folder/file.txt",
		Replicas: []*model.Replica{
			{
				Provider:  model.ProviderMicrosoft,
				AccountID: "user@example.com",
				Path:      "\\folder\\old\\file.txt",
				Status:    "active",
			},
			{
				Provider:  model.ProviderMicrosoft,
				AccountID: "user@example.com",
				Path:      "/folder/file.txt",
				Status:    "deleted",
			},
		},
	}

	if hasActiveMicrosoftReplicaAtPath(file, "user@example.com", "/folder/file.txt") {
		t.Fatalf("expected stale old-path replica not to satisfy canonical path presence")
	}

	file.Replicas = append(file.Replicas, &model.Replica{
		Provider:  model.ProviderMicrosoft,
		AccountID: "user@example.com",
		Path:      "\\folder\\file.txt",
		Status:    "active",
	})

	if !hasActiveMicrosoftReplicaAtPath(file, "user@example.com", "/folder/file.txt") {
		t.Fatalf("expected active canonical-path replica to satisfy presence check")
	}
}

func TestDistributeShortcutsAcrossMSAccountsSchedulesCanonicalPathRepair(t *testing.T) {
	r := NewRunner(&model.Config{}, nil, true)
	msUser := model.User{Provider: model.ProviderMicrosoft, Email: "user@example.com"}
	msFile := &model.File{
		ID:   "file-1",
		Path: "/folder/file.txt",
		Name: "file.txt",
		Replicas: []*model.Replica{
			{
				Provider:  model.ProviderMicrosoft,
				AccountID: "user@example.com",
				Path:      "/folder/old/file.txt",
				Status:    "active",
			},
		},
	}
	filesByPath := map[string]map[model.Provider][]*model.File{
		"/folder/file.txt": {
			model.ProviderMicrosoft: {msFile},
		},
	}

	refreshTargets := r.distributeShortcutsAcrossMSAccounts([]model.User{msUser}, filesByPath, 0)
	if len(refreshTargets) != 0 {
		t.Fatalf("expected safe mode to avoid creating shortcuts, got %d refresh targets", len(refreshTargets))
	}
}
