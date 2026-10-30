package httpapi

import (
	"strings"
	"testing"
)

func TestValidateAttachmentAcceptsAllowedTypesAndSizes(t *testing.T) {
	cases := []struct {
		name string
		ext  string
		size int64
		ok   bool
	}{
		{"text small", "notes.txt", 100, true},
		{"markdown small", "brand.md", 100, true},
		{"pdf small", "deck.pdf", 100, true},
		{"too large", "notes.txt", maxAttachmentBytes + 1, false},
		{"wrong type", "image.png", 100, false},
		{"no ext", "README", 100, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateAttachment(c.ext, c.size)
			if c.ok && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("expected reject")
			}
		})
	}
}

func TestPathExtHandlesMultipleDots(t *testing.T) {
	if got := pathExt("brand.v2.txt"); got != ".txt" {
		t.Fatalf("got %q, want .txt", got)
	}
	if got := pathExt("deck.PDF"); got != ".PDF" {
		t.Fatalf("got %q, want .PDF", got)
	}
	if got := pathExt("noext"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestExtractTextReadsTextAndMarkdownOnly(t *testing.T) {
	if got := extractText("a.txt", []byte("hello")); got != "hello" {
		t.Fatalf("txt: %q", got)
	}
	if got := extractText("a.md", []byte("# hi")); got != "# hi" {
		t.Fatalf("md: %q", got)
	}
	if got := extractText("a.pdf", []byte("binary")); got != "" {
		t.Fatalf("pdf should extract no text, got %q", got)
	}
}

func TestValidateAttachmentRejectsOversizedBeforeUpload(t *testing.T) {
	err := validateAttachment("big.txt", maxAttachmentBytes+1)
	if err == nil {
		t.Fatal("expected size rejection")
	}
	if !strings.Contains(err.Error(), "2 MB") {
		t.Fatalf("unexpected message: %v", err)
	}
}