// knowledge.go — REST endpoints for the RAG layer.
//
// Endpoints (all under /api/v1):
//
//	GET    /knowledge                       — list KBs (with counts)
//	POST   /knowledge                       — create KB (body: name, description, embedding_provider, embedding_model)
//	DELETE /knowledge/:kb                   — drop KB + all docs/chunks
//	GET    /knowledge/:kb/documents         — list documents in a KB
//	POST   /knowledge/:kb/documents         — ingest a document (JSON body or multipart upload)
//	DELETE /knowledge/:kb/documents/:doc    — delete a single document
//	POST   /knowledge/:kb/search            — quick search (used by the GUI's test box)
//
// All handlers require the API key middleware applied by the parent router.
package gateway

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/knowledge"
)

// handleListKnowledge returns every KB in the store.
func (s *Server) handleListKnowledge(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.JSON(fiber.Map{"knowledge_bases": []any{}, "enabled": false})
	}
	kbs, err := svc.Store.ListKBs()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{
		"knowledge_bases":            kbs,
		"enabled":                    true,
		"default_embedding_provider": s.cfg.Knowledge.EmbeddingProvider,
		"default_embedding_model":    s.cfg.Knowledge.EmbeddingModel,
	})
}

// handleCreateKnowledge creates a new KB. The embedding dim is probed against
// the chosen embedder, so the user only needs to pick a provider+model.
func (s *Server) handleCreateKnowledge(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "knowledge store disabled (set knowledge.db_path in config.yaml)"})
	}
	var body struct {
		Name              string `json:"name"`
		Description       string `json:"description"`
		EmbeddingProvider string `json:"embedding_provider"`
		EmbeddingModel    string `json:"embedding_model"`
		ChunkSize         int    `json:"chunk_size"`
		ChunkOverlap      int    `json:"chunk_overlap"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name is required"})
	}
	if body.EmbeddingProvider == "" {
		body.EmbeddingProvider = s.cfg.Knowledge.EmbeddingProvider
	}
	if body.EmbeddingModel == "" {
		body.EmbeddingModel = s.cfg.Knowledge.EmbeddingModel
	}
	if body.ChunkSize == 0 {
		body.ChunkSize = s.cfg.Knowledge.ChunkSize
		if body.ChunkSize == 0 {
			body.ChunkSize = 1000
		}
	}
	if body.ChunkOverlap == 0 {
		body.ChunkOverlap = s.cfg.Knowledge.ChunkOverlap
	}

	embedder := svc.Embedders.Get(body.EmbeddingProvider)
	if embedder == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("embedding provider %q not registered (available: %v)", body.EmbeddingProvider, svc.Embedders.IDs()),
		})
	}

	dim, err := embedder.Dim(c.Context(), body.EmbeddingModel)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": fmt.Sprintf("probe embedding model %q on %q: %v", body.EmbeddingModel, body.EmbeddingProvider, err),
		})
	}

	kb, err := svc.Store.CreateKB(knowledge.KB{
		Name:              body.Name,
		Description:       body.Description,
		EmbeddingProvider: body.EmbeddingProvider,
		EmbeddingModel:    body.EmbeddingModel,
		Dim:               dim,
		ChunkSize:         body.ChunkSize,
		ChunkOverlap:      body.ChunkOverlap,
	})
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("knowledge: kb created", zap.String("name", kb.Name), zap.Int("dim", kb.Dim))
	return c.Status(fiber.StatusCreated).JSON(kb)
}

// handleDeleteKnowledge drops a KB.
func (s *Server) handleDeleteKnowledge(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}
	name := c.Params("kb")
	if err := svc.Store.DeleteKB(name); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("knowledge: kb deleted", zap.String("name", name))
	return c.SendStatus(fiber.StatusNoContent)
}

// handleListKnowledgeDocuments returns documents in a KB.
func (s *Server) handleListKnowledgeDocuments(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.JSON(fiber.Map{"documents": []any{}})
	}
	kb, err := svc.Store.GetKB(c.Params("kb"))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if kb == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "kb not found"})
	}
	docs, err := svc.Store.ListDocuments(kb.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"kb": kb, "documents": docs})
}

// handleIngestDocument adds a document to a KB. Accepts either:
//   - JSON  { "title": "...", "source": "...", "content": "..." }
//   - multipart form: file field "file" (filename used as title/source).
//
// Parent-child chunking:
//
// To maximise retrieval recall without inflating context windows, documents are
// chunked at two granularities:
//
//   - Parent chunks (context windows): ~4× chunk_size characters each. These
//     are stored in the chunks table but carry NO embedding and are never
//     inserted into the vec0 or FTS5 tables. They exist purely to provide a
//     wider excerpt to the LLM after a child retrieval hit. Parents have no
//     parent_chunk_id (NULL).
//
//   - Child chunks (retrieval windows): chunk_size characters each, with
//     chunk_overlap overlap. These are embedded and inserted into both vec0
//     (for KNN) and chunks_fts (for BM25). Each child references the parent
//     that covers it via parent_chunk_id.
//
// Parent assignment uses character midpoint: for each child chunk the handler
// computes the rune offset of its midpoint within the full document text and
// advances a cursor through the cumulative rune lengths of the parent chunks
// until it finds the one whose range covers that midpoint. This is O(n) in the
// number of chunks and requires only the pre-computed parent end-rune positions.
//
// At query time, Store.Search resolves the parent via a LEFT JOIN and returns
// parent.content (the wide window) rather than child.content (the narrow one),
// giving the LLM sufficient surrounding context without embedding the full
// parent text.
func (s *Server) handleIngestDocument(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "knowledge store disabled"})
	}
	kb, err := svc.Store.GetKB(c.Params("kb"))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if kb == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "kb not found"})
	}

	var (
		title    string
		source   string
		mimeType string
		data     []byte
	)

	ctype := strings.ToLower(c.Get("Content-Type"))
	switch {
	case strings.HasPrefix(ctype, "multipart/form-data"):
		fh, ferr := c.FormFile("file")
		if ferr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": fmt.Sprintf("multipart upload missing 'file': %v", ferr)})
		}
		f, oerr := fh.Open()
		if oerr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": oerr.Error()})
		}
		raw, rerr := io.ReadAll(f)
		f.Close()
		if rerr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": rerr.Error()})
		}
		data = raw
		title = c.FormValue("title")
		if title == "" {
			title = fh.Filename
		}
		source = fh.Filename
		mimeType = knowledge.MimeFromFilename(fh.Filename)
		if mimeType == "" {
			mimeType = fh.Header.Get("Content-Type")
		}
	default:
		var body struct {
			Title    string `json:"title"`
			Source   string `json:"source"`
			MIMEType string `json:"mime_type"`
			Content  string `json:"content"`
		}
		if perr := c.BodyParser(&body); perr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": perr.Error()})
		}
		if body.Content == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "content is required"})
		}
		title = body.Title
		source = body.Source
		mimeType = body.MIMEType
		data = []byte(body.Content)
	}

	if title == "" {
		title = "Untitled document"
	}

	text, err := knowledge.ExtractText(mimeType, data)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": fmt.Sprintf("extract text: %v", err)})
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "document produced no extractable text"})
	}

	embedder := svc.Embedders.Get(kb.EmbeddingProvider)
	if embedder == nil {
		return c.Status(fiber.StatusFailedDependency).JSON(fiber.Map{
			"error": fmt.Sprintf("embedder %q not registered", kb.EmbeddingProvider),
		})
	}

	// Parent chunks: large windows for LLM context (returned to agent after retrieval).
	parentSize := kb.ChunkSize * 4
	if parentSize < 1 {
		parentSize = 4000
	}
	parentTexts := knowledge.ChunkText(text, parentSize, kb.ChunkOverlap*2)

	// Child chunks: smaller windows for embedding/retrieval.
	childTexts := knowledge.ChunkText(text, kb.ChunkSize, kb.ChunkOverlap)

	if len(childTexts) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no chunks produced (empty document?)"})
	}

	// Embed only child chunks in batches to avoid huge payloads on big files.
	const batchSize = 64
	vecs := make([][]float32, 0, len(childTexts))
	for i := 0; i < len(childTexts); i += batchSize {
		end := i + batchSize
		if end > len(childTexts) {
			end = len(childTexts)
		}
		ctx2, cancel := context.WithCancel(c.Context())
		batch, eerr := embedder.Embed(ctx2, kb.EmbeddingModel, childTexts[i:end])
		cancel()
		if eerr != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"error": fmt.Sprintf("embed batch %d-%d: %v", i, end, eerr),
			})
		}
		vecs = append(vecs, batch...)
	}

	// Pre-assign UUIDs for parent chunks so children can reference them.
	parentIDs := make([]string, len(parentTexts))
	for i := range parentTexts {
		parentIDs[i] = uuid.New().String()
	}

	// Assign each child to the parent whose character range covers the child's midpoint.
	// Track cumulative rune positions for parent boundaries.
	parentEndRunes := make([]int, len(parentTexts))
	{
		pos := 0
		for i, p := range parentTexts {
			pos += len([]rune(p))
			parentEndRunes[i] = pos
		}
	}

	allChunks := make([]knowledge.Chunk, 0, len(parentTexts)+len(childTexts))
	// Add parent chunks first (no vector — zero-length Vector means no vec insert).
	for i, pt := range parentTexts {
		allChunks = append(allChunks, knowledge.Chunk{ID: parentIDs[i], Content: pt})
	}
	// Add child chunks, each linked to its parent.
	childRunePos := 0
	parentIdx := 0
	for i, ct := range childTexts {
		cr := len([]rune(ct))
		mid := childRunePos + cr/2
		for parentIdx < len(parentEndRunes)-1 && mid > parentEndRunes[parentIdx] {
			parentIdx++
		}
		allChunks = append(allChunks, knowledge.Chunk{
			Content:       ct,
			ParentChunkID: parentIDs[parentIdx],
			Vector:        vecs[i],
		})
		childRunePos += cr
	}

	doc, err := svc.Store.AddDocument(kb, knowledge.Document{
		Title:    title,
		Source:   source,
		MIMEType: mimeType,
		ByteSize: int64(len(data)),
	}, allChunks)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("knowledge: doc ingested",
		zap.String("kb", kb.Name),
		zap.String("title", title),
		zap.Int("chunks", len(allChunks)),
	)
	return c.Status(fiber.StatusCreated).JSON(doc)
}

// handleDeleteKnowledgeDocument removes a single document and its chunks.
func (s *Server) handleDeleteKnowledgeDocument(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}
	kb, err := svc.Store.GetKB(c.Params("kb"))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if kb == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "kb not found"})
	}
	if err := svc.Store.DeleteDocument(kb.ID, c.Params("doc")); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// handleSearchKnowledge runs a one-shot search for the GUI's debug box.
func (s *Server) handleSearchKnowledge(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "knowledge store disabled"})
	}
	var body struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if body.TopK <= 0 {
		body.TopK = 5
	}
	kbName := c.Params("kb")
	kb, err := svc.Store.GetKB(kbName)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if kb == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "kb not found"})
	}

	embedder := svc.Embedders.Get(kb.EmbeddingProvider)
	if embedder == nil {
		return c.Status(fiber.StatusFailedDependency).JSON(fiber.Map{"error": "embedder not registered"})
	}
	vecs, err := embedder.Embed(c.Context(), kb.EmbeddingModel, []string{body.Query})
	if err != nil || len(vecs) == 0 {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": fmt.Sprintf("embed query: %v", err)})
	}
	hits, err := svc.Store.Search(kb, vecs[0], body.TopK)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"hits": hits})
}
