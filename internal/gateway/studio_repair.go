package gateway

import (
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/studio"
)

// studio_repair.go — post-run "learn from Run Live and adjust" endpoints.
//
//	POST /studio/repair-live  — given a draft + the last run's per-node trace,
//	                            return reviewable repair proposals (deterministic
//	                            shape adapters first, LLM rewrite for novel shapes).
//	POST /studio/apply-repair — apply ONE approved proposal to the draft and
//	                            re-validate, returning the patched draft. Nothing
//	                            is auto-applied; the client drives approval.

type repairLiveRequest struct {
	Workflow  studio.Draft          `json:"workflow"`
	NodeTrace []repairTraceNodeJSON `json:"node_trace"`
}

type repairTraceNodeJSON struct {
	NodeID     string `json:"node_id"`
	Kind       string `json:"kind"`
	Input      string `json:"input"`
	Output     string `json:"output"`
	InputFull  string `json:"input_full"`
	OutputFull string `json:"output_full"`
	Error      string `json:"error"`
}

// pick returns the full field when present, else the truncated one — so repair
// diagnosis always uses the most complete data the client has.
func pick(full, short string) string {
	if strings.TrimSpace(full) != "" {
		return full
	}
	return short
}

// handleStudioRepairLive turns a live trace into repair proposals. It maps each
// traced node back to its draft Output var so the shape diagnosis can find the
// producer of a mismatched value. A missing LLM still yields deterministic
// proposals; only novel shapes need the model.
func (s *Server) handleStudioRepairLive(c *fiber.Ctx) error {
	var req repairLiveRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body: "+err.Error())
	}
	// node id → Output var, from the draft.
	outVar := map[string]string{}
	for _, n := range req.Workflow.Flow.Nodes {
		outVar[n.ID] = n.Output
	}
	runs := make([]studio.LiveNodeRun, 0, len(req.NodeTrace))
	for _, t := range req.NodeTrace {
		runs = append(runs, studio.LiveNodeRun{
			NodeID:    t.NodeID,
			Kind:      t.Kind,
			Input:     pick(t.InputFull, t.Input),
			Output:    toRawJSON(pick(t.OutputFull, t.Output)),
			Error:     t.Error,
			OutputVar: outVar[t.NodeID],
		})
	}

	proposals := studio.ProposeLiveRepairs(c.Context(), s.studioLLM(), req.Workflow, runs)
	if proposals == nil {
		proposals = []studio.RepairProposal{}
	}
	return c.JSON(fiber.Map{"proposals": proposals})
}

type applyRepairRequest struct {
	Workflow studio.Draft          `json:"workflow"`
	Proposal studio.RepairProposal `json:"proposal"`
}

// handleStudioApplyRepair applies one approved proposal and re-validates. The
// deterministic re-check (NormalizeAndCheck) confirms the patched node still
// compiles and its template parses before the client saves.
func (s *Server) handleStudioApplyRepair(c *fiber.Ctx) error {
	var req applyRepairRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body: "+err.Error())
	}
	d := req.Workflow
	if !studio.ApplyProposal(&d, req.Proposal) {
		return fiber.NewError(fiber.StatusBadRequest, "proposal did not match any node/field")
	}
	raw, _ := json.Marshal(d)
	check := studio.NormalizeAndCheck(string(raw))
	// Learn from this accepted repair (only when the patch validates) so future
	// generations avoid the same real-API shape mistake. Best-effort.
	if check.Valid {
		s.recordLessonFromRepair(req.Workflow, req.Proposal)
	}
	return c.JSON(fiber.Map{
		"workflow": d,
		"valid":    check.Valid,
		"errors":   check.Errors,
	})
}

// toRawJSON turns a traced output string into JSON: pass valid JSON through,
// otherwise wrap plain text as a JSON string so downstream shape checks are
// consistent (a non-JSON payload reads as a string value).
func toRawJSON(s string) json.RawMessage {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	if json.Valid([]byte(t)) {
		return json.RawMessage(t)
	}
	b, _ := json.Marshal(s)
	return b
}
