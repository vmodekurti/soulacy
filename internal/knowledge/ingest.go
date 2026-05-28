// ingest.go — text extraction + chunking pipeline for the knowledge store.
//
// We deliberately use a character-based chunker (no tokenizer) so the same
// pipeline works regardless of which embedding model is in play. Defaults:
//
//	chunk_size:    1000 characters (~250 tokens)
//	chunk_overlap: 200  characters
//
// Both are stored on the KB row so they can be tuned per-corpus later.
package knowledge

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// ExtractText returns the raw text content of a file given its mime type and
// raw bytes. Supported types: text/plain, text/markdown, application/pdf,
// application/vnd.openxmlformats-officedocument.wordprocessingml.document.
//
// Unknown mime types fall through to treating the bytes as utf-8 text so
// pasted content always works.
func ExtractText(mimeType string, data []byte) (string, error) {
	switch normalizeMime(mimeType) {
	case "text/plain", "text/markdown", "":
		return string(data), nil
	case "application/pdf":
		return extractPDF(data)
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return extractDOCX(data)
	default:
		// Unknown — best-effort treat as text.
		return string(data), nil
	}
}

// MimeFromFilename returns a best-effort mime type for the supplied filename.
// Lower-cases the extension; unknown extensions return "" so the caller can
// decide whether to default to text or refuse.
func MimeFromFilename(name string) string {
	name = strings.ToLower(name)
	switch {
	case strings.HasSuffix(name, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(name, ".docx"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case strings.HasSuffix(name, ".md"), strings.HasSuffix(name, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(name, ".txt"):
		return "text/plain"
	}
	return ""
}

func normalizeMime(m string) string {
	m = strings.TrimSpace(strings.ToLower(m))
	if idx := strings.Index(m, ";"); idx >= 0 {
		m = strings.TrimSpace(m[:idx])
	}
	return m
}

// ChunkText splits text into overlapping windows of `size` characters with
// `overlap` characters of overlap between consecutive chunks. Whitespace at
// chunk boundaries is trimmed; empty chunks are dropped.
//
// Named with the "Text" suffix to avoid colliding with the Chunk struct
// declared in store.go.
func ChunkText(text string, size, overlap int) []string {
	if size <= 0 {
		size = 1000
	}
	if overlap < 0 || overlap >= size {
		overlap = size / 5 // 20% default if user gave junk
	}

	// Operate on rune slice so we don't split multi-byte characters.
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	step := size - overlap
	if step <= 0 {
		step = size
	}

	var out []string
	for start := 0; start < len(runes); start += step {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		piece := strings.TrimSpace(string(runes[start:end]))
		if piece != "" {
			out = append(out, piece)
		}
		if end == len(runes) {
			break
		}
	}
	return out
}

// --- PDF -------------------------------------------------------------------

func extractPDF(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf: open: %w", err)
	}
	var sb strings.Builder
	totalPages := r.NumPage()
	for i := 1; i <= totalPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		// PlainText pulls flowing text out of the page; not perfect with
		// columns but adequate for most prose PDFs.
		t, err := page.GetPlainText(nil)
		if err != nil {
			// Some pages may be images or have unsupported font encodings.
			// Skip rather than fail the whole document.
			continue
		}
		sb.WriteString(t)
		// Page boundary marker — survives whitespace collapsing as a single
		// paragraph break.
		sb.WriteString("\n\n\n")
	}
	out := cleanPDFText(sb.String())
	if out == "" {
		return "", errors.New("pdf: no extractable text (scanned image? add OCR upstream)")
	}
	return out, nil
}

// cleanPDFText fixes the one-word-per-line fragmentation that ledongthuc/pdf
// produces: it emits each text-positioning operator on its own line, so a
// flowing paragraph comes out as "Claude\n\nMythos\n\nPreview\n\n…". For RAG
// quality we collapse all whitespace runs to a single space and preserve
// page-level paragraph breaks (we wrote a triple-newline marker between
// pages in extractPDF).
func cleanPDFText(s string) string {
	// 1. Normalise our page-boundary marker so it survives the collapse below.
	const pageBreak = "\x1fPAGEBREAK\x1f"
	s = strings.ReplaceAll(s, "\n\n\n", pageBreak)

	// 2. Collapse every other whitespace run (newlines, tabs, multiple spaces)
	//    into a single space. This rejoins the fragmented one-word-per-line
	//    output into normal flowing text.
	var sb strings.Builder
	sb.Grow(len(s))
	inWS := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inWS {
				sb.WriteByte(' ')
				inWS = true
			}
			continue
		}
		sb.WriteRune(r)
		inWS = false
	}

	// 3. Restore page breaks as double newlines so chunking still has natural
	//    boundaries to fall on.
	out := strings.ReplaceAll(sb.String(), pageBreak, "\n\n")
	return strings.TrimSpace(out)
}

// --- DOCX ------------------------------------------------------------------
//
// .docx files are zip archives containing word/document.xml. We unzip in-
// memory and walk the XML extracting <w:t> text nodes, inserting newlines
// at <w:p> boundaries.

type docxBody struct {
	XMLName xml.Name `xml:"document"`
	Body    struct {
		Paragraphs []struct {
			Runs []struct {
				Text string `xml:"t"`
			} `xml:"r"`
		} `xml:"p"`
	} `xml:"body"`
}

func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("docx: open zip: %w", err)
	}
	var docXML []byte
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("docx: open document.xml: %w", err)
			}
			docXML, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return "", err
			}
			break
		}
	}
	if docXML == nil {
		return "", errors.New("docx: word/document.xml not found")
	}

	// Stream-decode so we don't depend on namespace prefixes (encoding/xml
	// ignores them when struct tags drop the namespace).
	var body docxBody
	dec := xml.NewDecoder(bytes.NewReader(docXML))
	dec.Strict = false
	if err := dec.Decode(&body); err != nil {
		return "", fmt.Errorf("docx: decode: %w", err)
	}

	var sb strings.Builder
	for _, p := range body.Body.Paragraphs {
		for _, r := range p.Runs {
			sb.WriteString(r.Text)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String()), nil
}
