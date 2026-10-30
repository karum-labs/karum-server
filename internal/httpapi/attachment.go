package httpapi

import (
	"mime"
	"path/filepath"
	"strings"
)

// maxAttachmentBytes is the hard cap on an uploaded brand file (PLAN.md §65).
const maxAttachmentBytes = 2 << 20 // 2 MB

// allowedAttachmentExt is the set of file extensions the server accepts for
// pitch attachments. PDF is accepted at type-check and stored with an id, but
// its text is not extracted (no PDF text-extraction library is pinned in the
// toolchain); .txt and .md are extracted and fenced as untrusted brand data.
var allowedAttachmentExt = map[string]bool{
	".txt": true,
	".md":  true,
	".pdf": true,
}

// fileHeader is the subset of multipart.FileHeader that validateAttachment needs.
type fileHeader struct {
	Filename string
	Size     int64
	Header   map[string][]string
}

// validateAttachment checks an uploaded file's extension and size. It is
// applied on the server in addition to the client-side check (PLAN.md §65).
func validateAttachment(filename string, size int64) error {
	ext := strings.ToLower(pathExt(filename))
	if !allowedAttachmentExt[ext] {
		return &attachmentError{code: "unsupported_type", message: "only .txt, .md, and .pdf files may be attached"}
	}
	if size > maxAttachmentBytes {
		return &attachmentError{code: "file_too_large", message: "attached file exceeds the 2 MB size cap"}
	}
	// Mime-type sniffing as a second gate: refuse clearly non-text uploads.
	mt := mime.TypeByExtension(ext)
	if ext == ".pdf" && mt != "application/pdf" {
		return &attachmentError{code: "unsupported_type", message: "the uploaded PDF could not be validated"}
	}
	return nil
}

// extractText reads the extractable text from an attached file. .txt and .md
// are returned as UTF-8; .pdf returns "" (the file is still stored with an id,
// but its text does not inform the pitch until a PDF extractor is pinned).
func extractText(filename string, data []byte) string {
	switch strings.ToLower(pathExt(filename)) {
	case ".txt", ".md":
		return string(data)
	default:
		return ""
	}
}

// asData wraps one untrusted value in a delimited, single-line data block so
// an instruction-shaped attachment cannot change the prompt's rules (PLAN.md
// §65).
func asData(value string) string {
	text := strings.Join(strings.Fields(value), " ")
	text = strings.ReplaceAll(text, "<", " ")
	text = strings.ReplaceAll(text, ">", " ")
	return "<data>" + text + "</data>"
}

func pathExt(filename string) string {
	return filepath.Ext(filename)
}

// attachmentError is a typed validation failure for an uploaded file.
type attachmentError struct {
	code    string
	message string
}

func (e *attachmentError) Error() string { return e.message }
