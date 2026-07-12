// knowledge_jobs.go — the async ingestion API surface.
//
// Upload now returns 202 Accepted with a job, and these endpoints let the UI
// watch it: list jobs for a KB, poll one, and retry a failed one. Live progress
// is pushed over the existing WebSocket event hub, so the page doesn't have to
// poll at all.

package gateway

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/pkg/message"
)

// SetIngestWorker wires the background ingestion worker so uploads can wake it
// immediately instead of waiting out its poll interval. Follows the existing
// post-construction setter pattern (SetWorkboardStore / SetDLQStore).
func (s *Server) SetIngestWorker(w *knowledge.Worker) { s.ingestWorker = w }

// ingestSpoolDir is where uploaded bytes wait for the worker. Keeping them on
// disk (not in the job row, not in memory, not in the lossy in-memory queue) is
// what makes a 200 MB PDF cost a file path instead of a heap allocation.
func ingestSpoolDir() (string, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(ws.Data, "ingest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// enqueueIngest spools the upload and records a durable job.
func (s *Server) enqueueIngest(kbName, title, source, mimeType string, data []byte) (knowledge.IngestJob, error) {
	svc := s.engine.Knowledge()
	dir, err := ingestSpoolDir()
	if err != nil {
		return knowledge.IngestJob{}, err
	}
	id := uuid.New().String()
	spool := filepath.Join(dir, id+".bin")
	if err := os.WriteFile(spool, data, 0o600); err != nil {
		return knowledge.IngestJob{}, err
	}

	job, err := svc.Store.EnqueueIngest(knowledge.IngestJob{
		ID:        id,
		KBName:    kbName,
		Title:     title,
		Source:    source,
		MIMEType:  mimeType,
		SpoolPath: spool,
		ByteSize:  int64(len(data)),
	})
	if err != nil {
		_ = os.Remove(spool) // don't leave an orphaned spool file behind
		return knowledge.IngestJob{}, err
	}

	// Start it now rather than on the next poll tick.
	if s.ingestWorker != nil {
		s.ingestWorker.Nudge()
	}
	s.emitIngestJob(job)
	return job, nil
}

// handleListIngestJobs returns the ingestion queue for a KB (newest first).
func (s *Server) handleListIngestJobs(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "knowledge store disabled")
	}
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	jobs, err := svc.Store.ListIngests(knowledgeKBParam(c), limit)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if jobs == nil {
		jobs = []knowledge.IngestJob{}
	}
	return c.JSON(fiber.Map{"jobs": jobs})
}

// handleGetIngestJob returns one job (for polling clients).
func (s *Server) handleGetIngestJob(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "knowledge store disabled")
	}
	job, err := svc.Store.GetIngest(strings.TrimSpace(c.Params("job")))
	if err != nil {
		return s.errMsg(c, fiber.StatusNotFound, "ingest job not found")
	}
	return c.JSON(job)
}

// handleRetryIngestJob re-queues a failed job, resetting its attempt budget.
func (s *Server) handleRetryIngestJob(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "knowledge store disabled")
	}
	id := strings.TrimSpace(c.Params("job"))
	job, err := svc.Store.GetIngest(id)
	if err != nil {
		return s.errMsg(c, fiber.StatusNotFound, "ingest job not found")
	}
	if job.Status == knowledge.JobRunning || job.Status == knowledge.JobQueued {
		return s.errMsg(c, fiber.StatusConflict, "that ingestion is already in progress")
	}
	if _, err := os.Stat(job.SpoolPath); err != nil {
		return s.errMsg(c, fiber.StatusGone,
			"the uploaded file for this job is no longer on disk — re-upload the document")
	}

	retried, err := svc.Store.RequeueIngest(id, true) // operator retry resets attempts
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if s.ingestWorker != nil {
		s.ingestWorker.Nudge()
	}
	s.emitIngestJob(retried)
	return c.JSON(retried)
}

// ── progress → GUI ───────────────────────────────────────────────────────────

// ingestProgressSink adapts the EventHub to knowledge.ProgressSink so the
// knowledge package never has to import the gateway.
type ingestProgressSink struct{ s *Server }

// IngestProgress satisfies knowledge.ProgressSink.
func (p ingestProgressSink) IngestProgress(job knowledge.IngestJob) { p.s.emitIngestJob(job) }

// IngestProgressSink returns the sink to hand to the worker at wiring time.
func (s *Server) IngestProgressSink() knowledge.ProgressSink { return ingestProgressSink{s: s} }

// emitIngestJob broadcasts a job update to every connected GUI client over the
// existing /ws/events socket — no polling required.
func (s *Server) emitIngestJob(job knowledge.IngestJob) {
	if s.hub == nil {
		return
	}
	s.hub.Emit(message.Event{
		Type:      "knowledge.ingest",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"id":       job.ID,
			"kb":       job.KBName,
			"title":    job.Title,
			"status":   job.Status,
			"progress": job.Progress,
			"attempt":  job.Attempt,
			"error":    job.Error,
			"doc_id":   job.DocID,
		},
	})
}
