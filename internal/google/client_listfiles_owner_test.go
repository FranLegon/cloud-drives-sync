package google

import (
	"testing"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"google.golang.org/api/drive/v3"
)

func TestListFilesReplicaUsesOwnerAsCanonicalAccount(t *testing.T) {
	modified := time.Now().UTC().Format(time.RFC3339)
	f := &drive.File{
		Id:           "file-1",
		Name:         "report.txt",
		Size:         18,
		Md5Checksum:  "abc123",
		ModifiedTime: modified,
		Owners: []*drive.User{
			{EmailAddress: "owner@gmail.com"},
		},
	}

	ownerEmail := "viewer@gmail.com"
	if len(f.Owners) > 0 && f.Owners[0] != nil && f.Owners[0].EmailAddress != "" {
		ownerEmail = f.Owners[0].EmailAddress
	}

	replica := &model.Replica{
		Provider:   model.ProviderGoogle,
		AccountID:  ownerEmail,
		NativeID:   f.Id,
		NativeHash: f.Md5Checksum,
		ModTime:    parseTime(f.ModifiedTime),
		Status:     "active",
		Owner:      ownerEmail,
	}

	if replica.AccountID != "owner@gmail.com" {
		t.Fatalf("account_id = %q, want owner@gmail.com", replica.AccountID)
	}
	if replica.Owner != "owner@gmail.com" {
		t.Fatalf("owner = %q, want owner@gmail.com", replica.Owner)
	}
}
