package microsoft

import (
	"testing"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

func TestBuildAndParseFakeShortcutName(t *testing.T) {
	name := "report.final.v2.pdf"
	md5 := "d41d8cd98f00b204e9800998ecf8427e"

	placeholder := buildFakeShortcutName(name, md5)
	if placeholder != "report.final.v2.pdf.md5-d41d8cd98f00b204e9800998ecf8427e.placeholder" {
		t.Fatalf("placeholder = %q", placeholder)
	}

	gotName, gotMD5, ok := parseFakeShortcutName(placeholder)
	if !ok {
		t.Fatalf("parseFakeShortcutName returned ok=false")
	}
	if gotName != name {
		t.Fatalf("name = %q, want %q", gotName, name)
	}
	if gotMD5 != md5 {
		t.Fatalf("md5 = %q, want %q", gotMD5, md5)
	}
}

func TestParseFakeShortcutNameRejectsLegacySizeFormat(t *testing.T) {
	if _, _, ok := parseFakeShortcutName("report.pdf.sz-123.placeholder"); ok {
		t.Fatalf("legacy size placeholder should not match new parser")
	}
}

func TestCreateShortcutReplicaMetadataUsesShortcutHash(t *testing.T) {
	now := time.Unix(1234, 0)
	file := &model.File{
		ID:     "shortcut-id",
		Name:   "report.pdf",
		Size:   123,
		Status: "active",
		Replicas: []*model.Replica{
			{
				Name:       "report.pdf",
				Size:       123,
				Provider:   model.ProviderMicrosoft,
				AccountID:  "user@example.com",
				NativeID:   "shortcut-id",
				NativeHash: model.NativeHashShortcut,
				ModTime:    now,
				Status:     "active",
				Owner:      "SHARED",
			},
		},
	}

	if len(file.Replicas) != 1 {
		t.Fatalf("replicas = %d, want 1", len(file.Replicas))
	}
	if file.Replicas[0].NativeHash != model.NativeHashShortcut {
		t.Fatalf("native hash = %q, want %q", file.Replicas[0].NativeHash, model.NativeHashShortcut)
	}
	if file.Replicas[0].AccountID != "user@example.com" {
		t.Fatalf("account id = %q", file.Replicas[0].AccountID)
	}
}
