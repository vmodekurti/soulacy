// reasoning.go — Story 16: wiring the pluggable reasoning loops (E15) into
// the engine's live execution path.
//
// When an agent's SOUL.yaml declares a reasoning: block with a strategy, the
// engine runs the task through reasoning.Loop instead of the classic
// single-call tool loop. Tool calls issued by the loop are bridged back into
// Engine.runTool, so they respect the exact same dispatch order and policy
// surface as classic agents: MCP allowlists, plugin tools, peer-agent calls,
// built-in confirmation gates, the Python sandbox, audit logging, and SSRF
// protection. Agents without a reasoning block keep today's behaviour
// untouched.
//
// Step traces surface as engine events so the GUI thinking section and the
// activity feed can render them:
//
//	reasoning.start  {strategy, max_steps, tools}
//	reasoning.step   {index, thought, tool, observation, duration_ms} (per step)
//	reasoning.result {steps, confident, duration_ms}
//
// tool.call / tool.result events fire in real time from the executor bridge,
// exactly like the classic loop.
package runtime

import (
	"context"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// SetReasoningKeys wires cloud-provider API keys for reasoning LLM backends.
// Called from internal/app at boot; safe to call before traffic starts.
func (e *Engine) SetReasoningKeys(keys reasoning.ProviderKeys) {
	e.reasoningKeys = keys
}

// SetReasonerOverride sets the optional global llm.reasoner provider/model used
// by the reasoning loop for every agent. Empty strings clear the override (the
// loop then uses each agent's own llm.provider/model). Called at boot.
func (e *Engine) SetReasonerOverride(provider, model string) {
	e.reasonerProvider = strings.TrimSpace(provider)
	e.reasonerModel = strings.TrimSpace(model)
}

// SetReasoningBackendFactory overrides how the engine builds the reasoning
// LLM backend for an agent. nil (the default) means
// reasoning.DefaultBackendFor(def, keys). Used by tests to inject fakes and
// available as an extension point for embedders.
func (e *Engine) SetReasoningBackendFactory(f func(*agent.Definition) reasoning.LLMBackend) {
	e.reasoningBackendFactory = f
}

// reasoningToolExecutor bridges sdk/reasoning.ToolExecutor onto the engine's
// tool dispatch. Every call goes through Engine.runTool — the same path the
// classic loop uses — so sandboxing, audit, confirmation gates, and MCP/plugin
// allowlists all apply unchanged.
type reasoningToolExecutor struct {
	e         *Engine
	def       *agent.Definition
	sessionID string
}

func (x reasoningToolExecutor) Execute(ctx context.Context, call reasoning.ToolCall) reasoning.Observation {
	args := make(map[string]any, len(call.Input))
	for k, v := range call.Input {
		args[k] = v
	}
	tc := normalizeToolCall(message.ToolCall{
		ID:        "rsn-" + uuidShort(),
		Name:      call.Tool,
		Arguments: args,
	})

	x.e.sink.Emit(message.Event{
		Type: "tool.call", AgentID: x.def.ID, SessionID: x.sessionID,
		Payload: tc, Timestamp: time.Now().UTC(),
	})

	toolStart := time.Now()
	result, err := x.e.runTool(ctx, x.def, x.sessionID, tc)
	metrics.ToolCallDuration.WithLabelValues(tc.Name).Observe(time.Since(toolStart).Seconds())
	isErr := err != nil
	if isErr {
		result = "error: " + err.Error()
		metrics.ToolCallsTotal.WithLabelValues(tc.Name, "error").Inc()
	} else {
		metrics.ToolCallsTotal.WithLabelValues(tc.Name, "success").Inc()
	}

	x.e.sink.Emit(message.Event{
		Type: "tool.result", AgentID: x.def.ID, SessionID: x.sessionID,
		Payload:   message.ToolResult{CallID: tc.ID, Name: tc.Name, Content: result, IsError: isErr},
		Timestamp: time.Now().UTC(),
	})

	if err != nil {
		return reasoning.Observation{Error: err, Source: tc.Name}
	}
	return reasoning.Observation{Content: result, Source: tc.Name}
}

// handleWithReasoning runs one task through the reasoning Loop and finishes
// the run with the same persistence/reply contract as the classic path.
// loopCfg comes from reasoning.LoopConfigFromDefinition — the caller already
// verified the agent opted in.
func (e *Engine) handleWithReasoning(ctx context.Context, def *agent.Definition, sess *Session, msg message.Message, loopCfg reasoning.LoopConfig) (message.Message, error) {
	// Advertise the engine's FULL tool surface to the loop, not just the
	// agent's declared Python tools: built-ins (gated per agent/channel),
	// MCP tools, plugin tools, and peer agents — whatever allToolSchemas
	// would offer the classic loop.
	have := make(map[string]struct{}, len(loopCfg.ToolNames))
	for _, n := range loopCfg.ToolNames {
		have[n] = struct{}{}
	}
	for _, s := range e.allToolSchemas(def, msg.Channel) {
		if _, dup := have[s.Name]; !dup {
			loopCfg.ToolNames = append(loopCfg.ToolNames, s.Name)
			have[s.Name] = struct{}{}
		}
	}

	var backend reasoning.LLMBackend
	if e.reasoningBackendFactory != nil {
		backend = e.reasoningBackendFactory(def)
	} else {
		backend = reasoning.DefaultBackendFor(e.reasoningDef(def), e.reasoningKeys)
	}
	executor := reasoningToolExecutor{e: e, def: def, sessionID: msg.SessionID}
	loop := reasoning.New(loopCfg, backend, executor)

	e.sink.Emit(message.Event{
		Type: "reasoning.start", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"strategy":  string(loopCfg.Strategy),
			"max_steps": loopCfg.MaxSteps,
			"tools":     len(loopCfg.ToolNames),
		},
		Timestamp: time.Now().UTC(),
	})

	result := loop.Run(ctx, def.ID, flattenParts(msg.Parts))

	// Surface the think-act-observe trace. Loop.Run returns the full trace at
	// the end; tool.call/tool.result already streamed live from the bridge.
	for i, step := range result.Steps {
		obs := step.Obs.Content
		if len(obs) > 400 {
			obs = obs[:400] + "…"
		}
		e.sink.Emit(message.Event{
			Type: "reasoning.step", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"index":       i + 1,
				"thought":     step.Thought,
				"tool":        step.Action.Tool,
				"observation": obs,
				"duration_ms": step.Duration.Milliseconds(),
			},
			Timestamp: time.Now().UTC(),
		})
	}

	e.sink.Emit(message.Event{
		Type: "reasoning.result", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"steps":       len(result.Steps),
			"confident":   result.Confident,
			"duration_ms": result.Duration.Milliseconds(),
		},
		Timestamp: time.Now().UTC(),
	})

	// Story E23 — self-updating rulebooks, versioned. Reflect's
	// updated_rules persists ONLY when the agent opted in via
	// brain_memory.procedural.auto_update, always as a new immutable
	// version. A locked rulebook refuses the write (warn event, run
	// continues) — drift control beats self-tuning.
	if result.UpdatedRules != "" && def.BrainMemory.Procedural.AutoUpdate && e.brainStore != nil {
		if v, uerr := e.brainStore.UpdateProceduralVersioned(def.ID, result.UpdatedRules, "auto_update"); uerr != nil {
			e.sink.Emit(message.Event{
				Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "rulebook", "error": uerr.Error()},
				Timestamp: time.Now().UTC(),
			})
		} else {
			e.sink.Emit(message.Event{
				Type: "rulebook.updated", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"version": v, "source": "auto_update", "bytes": len(result.UpdatedRules)},
				Timestamp: time.Now().UTC(),
			})
		}
	}

	finalContent := result.Output
	if finalContent == "" {
		finalContent = "(no final response produced)"
		e.sink.Emit(message.Event{
			Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"stage": "reasoning", "error": "loop produced empty output"},
			Timestamp: time.Now().UTC(),
		})
	}

	return e.finalizeReply(ctx, def, sess, msg, finalContent), nil
}
