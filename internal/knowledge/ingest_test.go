// ingest_test.go — regression tests for the chunker and text-extraction
// pipeline. These are pure functions with no external dependencies, so they
// run fast and CI-friendly.
package knowledge

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestChunkText_BasicWindowing(t *testing.T) {
	text := strings.Repeat("abcdefghij", 300) // 3000 chars
	chunks := ChunkText(text, 1000, 200)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	// With size 1000 and step 800 (1000-200), 3000 chars → starts at 0, 800,
	// 1600, 2400 → 4 chunks (the last starts inside the doc and runs to 3000).
	if got, want := len(chunks), 4; got != want {
		t.Errorf("chunk count: got %d, want %d", got, want)
	}
	// First chunk should be 1000 chars (trimmed of any leading/trailing ws —
	// no ws here, so exactly 1000).
	if len(chunks[0]) != 1000 {
		t.Errorf("first chunk size: got %d, want 1000", len(chunks[0]))
	}
}

func TestChunkText_EmptyInputProducesNoChunks(t *testing.T) {
	if got := ChunkText("", 1000, 200); len(got) != 0 {
		t.Errorf("empty input: got %d chunks, want 0", len(got))
	}
	if got := ChunkText("   \n\t  ", 1000, 200); len(got) != 0 {
		t.Errorf("whitespace-only input: got %d chunks, want 0", len(got))
	}
}

func TestChunkText_BadSizeDefaultsApply(t *testing.T) {
	// size <= 0 should default to 1000.
	chunks := ChunkText("hello world", 0, 0)
	if len(chunks) != 1 || chunks[0] != "hello world" {
		t.Errorf("zero size: expected single chunk 'hello world', got %v", chunks)
	}
	// overlap >= size should clamp to size/5.
	chunks = ChunkText(strings.Repeat("x", 500), 100, 100)
	if len(chunks) < 5 {
		t.Errorf("overlap clamping: expected ≥5 chunks for clamped overlap, got %d", len(chunks))
	}
}

func TestChunkText_MultiByteRunesNotSplit(t *testing.T) {
	// Each emoji is one rune but multiple bytes. The chunker should split on
	// runes, not bytes, so an emoji is never fractured.
	text := strings.Repeat("🦀", 500) // 500 runes (~2000 bytes)
	chunks := ChunkText(text, 100, 20)
	for i, c := range chunks {
		// Decoding the chunk back to runes should match its rune count cleanly
		// (no replacement characters).
		if strings.ContainsRune(c, '�') {
			t.Errorf("chunk %d contained replacement char — multi-byte split", i)
		}
	}
}

func TestExtractText_Passthrough(t *testing.T) {
	cases := []struct {
		mime, body string
	}{
		{"text/plain", "hello world"},
		{"text/markdown", "# heading\n\nbody"},
		{"", "no mime"}, // unknown mime → passthrough
		{"text/plain; charset=utf-8", "with params"},
	}
	for _, c := range cases {
		got, err := ExtractText(c.mime, []byte(c.body))
		if err != nil {
			t.Errorf("ExtractText(%q): unexpected error %v", c.mime, err)
			continue
		}
		if got != c.body {
			t.Errorf("ExtractText(%q): got %q, want %q", c.mime, got, c.body)
		}
	}
}

func TestMimeFromFilename(t *testing.T) {
	cases := []struct{ name, want string }{
		{"foo.pdf", "application/pdf"},
		{"FOO.PDF", "application/pdf"}, // case-insensitive
		{"report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"notes.md", "text/markdown"},
		{"notes.markdown", "text/markdown"},
		{"log.txt", "text/plain"},
		{"unknown.xyz", ""},
		{"no-extension", ""},
	}
	for _, c := range cases {
		if got := MimeFromFilename(c.name); got != c.want {
			t.Errorf("MimeFromFilename(%q): got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestCleanPDFText covers the fix for ledongthuc/pdf's one-word-per-line
// fragmentation. The cleaner must collapse single-word lines back into
// flowing text while preserving page-level paragraph breaks.
func TestCleanPDFText(t *testing.T) {
	input := "Claude\n\nMythos\n\nPreview" // typical fragmented output
	got := cleanPDFText(input)
	want := "Claude Mythos Preview"
	if got != want {
		t.Errorf("cleanPDFText fragmented input:\n  got:  %q\n  want: %q", got, want)
	}

	// Triple-newline page markers should produce one double-newline paragraph break.
	pageInput := "page one text\n\n\npage two text"
	got = cleanPDFText(pageInput)
	if !strings.Contains(got, "\n\n") {
		t.Errorf("cleanPDFText should preserve page boundary as \\n\\n, got %q", got)
	}
	if strings.Count(got, "\n") > 2 {
		t.Errorf("cleanPDFText should collapse all other whitespace; got %q", got)
	}
}

// extractDOCX is exercised via a synthetic minimal .docx in memory. We avoid
// shipping a binary fixture by building the zip + word/document.xml inline.
func TestExtractDOCX_RoundTrip(t *testing.T) {
	body := buildMinimalDOCX(t, []string{
		"First paragraph.",
		"Second paragraph with bold word.",
		"Third paragraph.",
	})
	text, err := ExtractText("application/vnd.openxmlformats-officedocument.wordprocessingml.document", body)
	if err != nil {
		t.Fatalf("ExtractText docx: %v", err)
	}
	for _, want := range []string{"First paragraph.", "Second paragraph", "Third paragraph."} {
		if !strings.Contains(text, want) {
			t.Errorf("expected DOCX output to contain %q, got %q", want, text)
		}
	}
}

// buildMinimalDOCX writes a zip containing a one-file word/document.xml whose
// body has the given paragraphs. Mirrors the minimum structure Office produces.
func buildMinimalDOCX(t *testing.T, paragraphs []string) []byte {
	t.Helper()
	type run struct {
		XMLName xml.Name `xml:"r"`
		Text    string   `xml:"t"`
	}
	type para struct {
		XMLName xml.Name `xml:"p"`
		Runs    []run    `xml:"r"`
	}
	type body struct {
		XMLName xml.Name `xml:"body"`
		Paras   []para
	}
	type doc struct {
		XMLName xml.Name `xml:"document"`
		Body    body
	}

	d := doc{}
	for _, p := range paragraphs {
		d.Body.Paras = append(d.Body.Paras, para{Runs: []run{{Text: p}}})
	}
	xmlBytes, err := xml.Marshal(d)
	if err != nil {
		t.Fatalf("marshal xml: %v", err)
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := f.Write(xmlBytes); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
