package microsoft

import "testing"

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
