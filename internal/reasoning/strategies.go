// strategies.go — the built-in reasoning strategies, registered with the
// SDK factory registry (Story E15) exactly like channel/provider drivers
// (E10): init() self-registration, resolved by name at Loop.Run time.
// Custom strategies follow the same pattern from their own packages.
package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdkreasoning "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
)

func init() {
	registry.MustRegisterReasoningStrategy(StrategyReAct, func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return reactStrategy{}, nil
	})
	registry.MustRegisterReasoningStrategy(StrategyPlanExecute, func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return planExecuteStrategy{}, nil
	})
}

// ─── ReAct ────────────────────────────────────────────────────────────────────

// reactStrategy runs interleaved think/act/observe cycles until the LLM
// reports IsDone, MaxSteps is exhausted, or the context expires — then
// reflects on whatever trace exists.
type reactStrategy struct{}

const (
	maxConsecutiveThinkErrors = 4
	maxTotalThinkErrors       = 8
)

func (reactStrategy) Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse) {
	var steps []Step
	consecutiveThinkErrors := 0
	totalThinkErrors := 0
	lastFailedToolKey := ""
	repeatedFailedToolCalls := 0

	for i := 0; i < env.Config.MaxSteps; i++ {
		if ctx.Err() != nil {
			break
		}

		stepCtx, cancel := context.WithTimeout(ctx, env.Config.StepTimeout)
		stepStart := time.Now()

		think, err := env.LLM.Think(stepCtx, ThinkRequest{
			TaskInput:    taskInput,
			StepHistory:  steps,
			SystemPrompt: env.Config.SystemPrompt,
			ToolNames:    env.Config.ToolNames,
		})
		if err != nil {
			consecutiveThinkErrors++
			totalThinkErrors++
			steps = append(steps, Step{
				ID:      fmt.Sprintf("step-%d", i+1),
				Thought: "The model returned an invalid reasoning step.",
				Obs: Observation{
					Content: thinkErrorInstruction(err, consecutiveThinkErrors, totalThinkErrors),
					Source:  "controller",
				},
				Duration: time.Since(stepStart),
			})
			cancel()
			if consecutiveThinkErrors >= 1 && lastUsefulObservation(steps) != "" {
				if resp, ok := reflectAfterRepeatedThinkErrors(ctx, env, taskInput, steps); ok {
					return steps, resp
				}
				if consecutiveThinkErrors >= 2 {
					return steps, ReflectResponse{Output: synthesizeAfterThinkErrors(steps)}
				}
			}
			if consecutiveThinkErrors >= maxConsecutiveThinkErrors || totalThinkErrors >= maxTotalThinkErrors {
				return steps, ReflectResponse{Output: thinkErrorStopMessage(steps)}
			}
			continue
		}
		consecutiveThinkErrors = 0

		if think.IsDone {
			if call, ok := recoverTextualToolCall(think.FinalAnswer, env.Config.ToolNames); ok {
				obs := env.Tools.Execute(stepCtx, call)
				obs = boundObservation(obs)
				steps = append(steps, Step{
					ID:       fmt.Sprintf("step-%d", i+1),
					Thought:  firstNonEmpty(think.Thought, "Recovered plain-text tool call from final_answer."),
					Action:   call,
					Obs:      obs,
					Duration: time.Since(stepStart),
				})
				cancel()
				continue
			}
			if isPrematureFinalAnswer(think.FinalAnswer) && i < env.Config.MaxSteps-1 {
				steps = append(steps, Step{
					ID:      fmt.Sprintf("step-%d", i+1),
					Thought: firstNonEmpty(think.Thought, "The model returned a progress note instead of a final answer."),
					Obs: Observation{
						Content: "controller: that was a progress note, not a completed result. Continue by making the next concrete tool call; do not say you are proceeding unless the work is actually complete.",
						Source:  "controller",
					},
				})
				cancel()
				continue
			}
			cancel()
			resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
				TaskInput:    taskInput,
				Steps:        steps,
				SystemPrompt: env.Config.SystemPrompt,
				OutputFormat: env.Config.OutputFormat,
			})
			if resp.Output == "" && think.FinalAnswer != "" {
				resp.Output = think.FinalAnswer
			}
			if call, ok := recoverTextualToolCall(resp.Output, env.Config.ToolNames); ok {
				recoveredCtx, recoveredCancel := context.WithTimeout(ctx, env.Config.StepTimeout)
				recoveredStart := time.Now()
				obs := env.Tools.Execute(recoveredCtx, call)
				recoveredCancel()
				obs = boundObservation(obs)
				steps = append(steps, Step{
					ID:       fmt.Sprintf("step-%d", i+1),
					Thought:  firstNonEmpty(think.Thought, "Recovered plain-text tool call from reflected output."),
					Action:   call,
					Obs:      obs,
					Duration: time.Since(recoveredStart),
				})
				continue
			}
			if isPrematureFinalAnswer(resp.Output) && i < env.Config.MaxSteps-1 {
				steps = append(steps, Step{
					ID:      fmt.Sprintf("step-%d", i+1),
					Thought: firstNonEmpty(think.Thought, "The model reflected a progress note instead of a final answer."),
					Obs: Observation{
						Content: "controller: reflected output was a progress note, not a completed result. Continue by making the next concrete tool call.",
						Source:  "controller",
					},
				})
				continue
			}
			return steps, resp
		}

		// Execute the tool — failures are wrapped as observations, not panics.
		obs := env.Tools.Execute(stepCtx, think.Action)
		obs = boundObservation(obs)
		callKey := toolCallKey(think.Action)
		repeatedToolFailure := false
		if isToolFailure(obs) && callKey != "" {
			if callKey == lastFailedToolKey {
				repeatedFailedToolCalls++
			} else {
				lastFailedToolKey = callKey
				repeatedFailedToolCalls = 1
			}
			repeatedToolFailure = repeatedFailedToolCalls >= 2
		} else {
			lastFailedToolKey = ""
			repeatedFailedToolCalls = 0
		}

		steps = append(steps, Step{
			ID:       fmt.Sprintf("step-%d", i+1),
			Thought:  think.Thought,
			Action:   think.Action,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		cancel()
		if repeatedToolFailure {
			steps = append(steps, Step{
				ID:      fmt.Sprintf("step-%d-recovery", i+1),
				Thought: "Repeated identical tool failure detected.",
				Obs: Observation{
					Content: repeatedToolFailureInstruction(think.Action, obs, repeatedFailedToolCalls),
					Source:  "controller",
				},
			})
			if repeatedFailedToolCalls >= 3 {
				if resp, ok := reflectAfterRepeatedToolFailures(ctx, env, taskInput, steps); ok {
					return steps, resp
				}
				return steps, ReflectResponse{Output: repeatedToolFailureStopMessage(steps)}
			}
		}
	}

	// MaxSteps exhausted or LLM errored — reflect on what we have.
	resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: env.Config.SystemPrompt,
		OutputFormat: env.Config.OutputFormat,
	})
	if call, ok := recoverTextualToolCall(resp.Output, env.Config.ToolNames); ok {
		stepCtx, cancel := context.WithTimeout(ctx, env.Config.StepTimeout)
		stepStart := time.Now()
		obs := env.Tools.Execute(stepCtx, call)
		cancel()
		obs = boundObservation(obs)
		steps = append(steps, Step{
			ID:       fmt.Sprintf("step-%d", len(steps)+1),
			Thought:  "Recovered plain-text tool call from reflected output.",
			Action:   call,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		resp, _ = env.LLM.Reflect(ctx, ReflectRequest{
			TaskInput:    taskInput,
			Steps:        steps,
			SystemPrompt: env.Config.SystemPrompt,
			OutputFormat: env.Config.OutputFormat,
		})
	}

	// Final guard: never end the run on a progress note ("I'll…", "Searching…").
	// If the terminal reflection is still a preamble, force one completion-only
	// synthesis; failing that, fall back to the last substantive observation so
	// the user gets the actual gathered result instead of an intent statement.
	if isPrematureFinalAnswer(resp.Output) {
		forced, _ := env.LLM.Reflect(ctx, ReflectRequest{
			TaskInput: taskInput,
			Steps:     steps,
			SystemPrompt: strings.TrimSpace(env.Config.SystemPrompt +
				"\n\nIMPORTANT: The task is complete. Output the FINISHED result now, using only what the steps already gathered. Do NOT describe what you are about to do or say you are proceeding — produce the completed answer itself."),
			OutputFormat: env.Config.OutputFormat,
		})
		if t := strings.TrimSpace(forced.Output); t != "" && !isPrematureFinalAnswer(t) {
			resp = forced
		} else if obs := lastUsefulObservation(steps); strings.TrimSpace(obs) != "" {
			resp.Output = obs
		}
	}
	return steps, resp
}

func thinkErrorInstruction(err error, consecutive, total int) string {
	return fmt.Sprintf("controller: Think failed (%s). Return one short valid JSON object only. Keep thought under 25 words. If a tool is needed, use action with concise arguments. Invalid response %d in a row, %d total this run.", err.Error(), consecutive, total)
}

func toolCallKey(call ToolCall) string {
	if strings.TrimSpace(call.Tool) == "" {
		return ""
	}
	args := call.Arguments
	if len(args) == 0 && len(call.Input) > 0 {
		args = make(map[string]any, len(call.Input))
		for k, v := range call.Input {
			args[k] = v
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return strings.TrimSpace(call.Tool)
	}
	return strings.TrimSpace(call.Tool) + ":" + string(raw)
}

func isToolFailure(obs Observation) bool {
	if obs.Error != nil {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(obs.Content)), "tool error:")
}

func repeatedToolFailureInstruction(call ToolCall, obs Observation, repeated int) string {
	msg := strings.TrimSpace(obs.Content)
	if msg == "" && obs.Error != nil {
		msg = obs.Error.Error()
	}
	if msg == "" {
		msg = "the tool failed without a message"
	}
	return fmt.Sprintf("controller: the exact same %q tool call failed %d times. Do not repeat it unchanged. Change the arguments, choose a different available tool, or finish with a concise explanation of the failure. Last failure: %s", call.Tool, repeated, truncateForPrompt(msg, 360))
}

func reflectAfterRepeatedToolFailures(ctx context.Context, env Env, taskInput string, steps []Step) (ReflectResponse, bool) {
	resp, err := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput: taskInput,
		Steps:     steps,
		SystemPrompt: strings.TrimSpace(env.Config.SystemPrompt +
			"\n\nIMPORTANT: The same tool call failed repeatedly. Do not propose running that identical call again. Produce the best available answer from the trace, or clearly explain what configuration/input is missing."),
		OutputFormat: env.Config.OutputFormat,
	})
	if err != nil || strings.TrimSpace(resp.Output) == "" {
		return ReflectResponse{}, false
	}
	if isPrematureFinalAnswer(resp.Output) {
		return ReflectResponse{}, false
	}
	return resp, true
}

func repeatedToolFailureStopMessage(steps []Step) string {
	last := lastUsefulObservation(steps)
	if last != "" {
		return "The run stopped because the same tool call failed repeatedly. Last useful observation: " + last
	}
	return "The run stopped because the same tool call failed repeatedly. Change the tool arguments, choose another tool, or verify the required channel/provider/credential configuration."
}

func thinkErrorStopMessage(steps []Step) string {
	last := lastUsefulObservation(steps)
	if last != "" {
		return "The run stopped because the model returned invalid ReAct JSON too many times. The last useful observation was: " + last
	}
	return "The run stopped because the model returned invalid ReAct JSON too many times before producing a usable tool result. Retry with a smaller input, choose a more reliable model, or switch this workflow step to a deterministic tool/flow node."
}

func synthesizeAfterThinkErrors(steps []Step) string {
	observations := recentUsefulObservations(steps, 3)
	if len(observations) == 0 {
		return thinkErrorStopMessage(steps)
	}
	var b strings.Builder
	b.WriteString("The run stopped because the model repeatedly returned invalid ReAct JSON after useful work was already completed. Here is the best available result from the successful steps:\n\n")
	for i, obs := range observations {
		if len(observations) > 1 {
			fmt.Fprintf(&b, "%d. %s\n", i+1, obs)
		} else {
			b.WriteString(obs)
		}
	}
	return b.String()
}

func recentUsefulObservations(steps []Step, max int) []string {
	if max <= 0 {
		return nil
	}
	reversed := make([]string, 0, max)
	for i := len(steps) - 1; i >= 0 && len(reversed) < max; i-- {
		s := steps[i]
		if s.Obs.Source == "controller" {
			continue
		}
		content := strings.TrimSpace(s.Obs.Content)
		if content == "" && s.Obs.Error != nil {
			content = s.Obs.Error.Error()
		}
		if content == "" {
			continue
		}
		reversed = append(reversed, truncateForPrompt(content, 420))
	}
	out := make([]string, len(reversed))
	for i := range reversed {
		out[len(reversed)-1-i] = reversed[i]
	}
	return out
}

func reflectAfterRepeatedThinkErrors(ctx context.Context, env Env, taskInput string, steps []Step) (ReflectResponse, bool) {
	resp, err := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: env.Config.SystemPrompt,
		OutputFormat: env.Config.OutputFormat,
	})
	if err != nil || strings.TrimSpace(resp.Output) == "" {
		return ReflectResponse{}, false
	}
	if isPrematureFinalAnswer(resp.Output) {
		return ReflectResponse{}, false
	}
	return resp, true
}

func lastUsefulObservation(steps []Step) string {
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		if s.Obs.Source == "controller" {
			continue
		}
		if strings.TrimSpace(s.Obs.Content) != "" {
			return truncateForPrompt(strings.TrimSpace(s.Obs.Content), 420)
		}
		if s.Obs.Error != nil {
			return truncateForPrompt(s.Obs.Error.Error(), 420)
		}
	}
	return ""
}

func truncateForPrompt(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

func recoverTextualToolCall(text string, toolNames []string) (ToolCall, bool) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	if call, ok := recoverJSONToolCall(s, toolNames); ok {
		return call, true
	}
	if call, ok := recoverActionInputToolCall(s, toolNames); ok {
		return call, true
	}
	if idx := strings.LastIndex(strings.ToLower(s), "action:"); idx >= 0 {
		s = strings.TrimSpace(s[idx+len("action:"):])
	}
	open := strings.Index(s, "(")
	close := strings.LastIndex(s, ")")
	if open <= 0 || close != len(s)-1 {
		return ToolCall{}, false
	}
	name := strings.TrimSpace(s[:open])
	if !toolAllowed(name, toolNames) {
		return ToolCall{}, false
	}
	rawArgs := strings.TrimSpace(s[open+1 : close])
	if rawArgs == "" {
		rawArgs = "{}"
	}
	var args map[string]any
	if parsed, ok := parseActionArgs(rawArgs); ok {
		args = parsed
	} else if strings.HasPrefix(rawArgs, "map[") && strings.HasSuffix(rawArgs, "]") {
		var ok bool
		args, ok = parseLegacyMapArgs(rawArgs)
		if !ok {
			return ToolCall{}, false
		}
	} else if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return ToolCall{}, false
	}
	input := make(map[string]string, len(args))
	for k, v := range args {
		switch t := v.(type) {
		case string:
			input[k] = t
		case nil:
			input[k] = ""
		case bool, float64:
			input[k] = fmt.Sprint(t)
		default:
			b, err := json.Marshal(t)
			if err != nil {
				return ToolCall{}, false
			}
			input[k] = string(b)
		}
	}
	return ToolCall{Tool: name, Input: input, Arguments: args}, true
}

func recoverJSONToolCall(s string, toolNames []string) (ToolCall, bool) {
	var direct ToolCall
	if err := json.Unmarshal([]byte(s), &direct); err == nil && direct.Tool != "" && toolAllowed(direct.Tool, toolNames) {
		normalizeToolCallArgs(&direct)
		return direct, true
	}

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ToolCall{}, false
	}
	raw := s[start : end+1]

	var wrapped struct {
		Tool        string         `json:"tool"`
		Name        string         `json:"name"`
		Action      any            `json:"action"`
		Input       map[string]any `json:"input"`
		Arguments   map[string]any `json:"arguments"`
		ActionInput map[string]any `json:"action_input"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		return ToolCall{}, false
	}

	name := firstNonEmpty(wrapped.Tool, wrapped.Name)
	args := firstNonNilMap(wrapped.Arguments, wrapped.Input, wrapped.ActionInput)

	switch a := wrapped.Action.(type) {
	case string:
		if name == "" {
			name = a
		}
	case map[string]any:
		if name == "" {
			name = firstString(a["tool"], a["name"])
		}
		if len(args) == 0 {
			args = firstMap(a["arguments"], a["input"], a["action_input"])
		}
	}

	if name == "" || !toolAllowed(name, toolNames) {
		return ToolCall{}, false
	}
	call := ToolCall{Tool: name, Arguments: args}
	normalizeToolCallArgs(&call)
	return call, true
}

func recoverActionInputToolCall(s string, toolNames []string) (ToolCall, bool) {
	lines := strings.Split(s, "\n")
	name := ""
	rawInput := ""
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "action", "tool":
			if name == "" {
				name = strings.TrimSpace(value)
			}
		case "action input", "input", "arguments":
			if rawInput == "" {
				rawInput = strings.TrimSpace(value)
			}
		}
	}
	if name == "" || !toolAllowed(name, toolNames) {
		return ToolCall{}, false
	}
	args := map[string]any{}
	if rawInput != "" {
		parsed, ok := parseActionArgs(rawInput)
		if !ok {
			return ToolCall{}, false
		}
		args = parsed
	}
	call := ToolCall{Tool: name, Arguments: args}
	normalizeToolCallArgs(&call)
	return call, true
}

func parseActionArgs(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, true
	}
	if strings.HasPrefix(raw, "input=") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "input="))
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err == nil {
			return args, true
		}
		return nil, false
	}
	if strings.HasPrefix(raw, "map[") && strings.HasSuffix(raw, "]") {
		return parseLegacyMapArgs(raw)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err == nil {
		return args, true
	}
	return nil, false
}

func normalizeToolCallArgs(call *ToolCall) {
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	if len(call.Arguments) == 0 && len(call.Input) > 0 {
		call.Arguments = make(map[string]any, len(call.Input))
		for k, v := range call.Input {
			call.Arguments[k] = v
		}
	}
	call.Input = make(map[string]string, len(call.Arguments))
	for k, v := range call.Arguments {
		switch t := v.(type) {
		case string:
			call.Input[k] = t
		case nil:
			call.Input[k] = ""
		case bool, float64:
			call.Input[k] = fmt.Sprint(t)
		default:
			b, err := json.Marshal(t)
			if err == nil {
				call.Input[k] = string(b)
			}
		}
	}
}

func firstString(values ...any) string {
	for _, v := range values {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstMap(values ...any) map[string]any {
	for _, v := range values {
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			return m
		}
	}
	return nil
}

func firstNonNilMap(values ...map[string]any) map[string]any {
	for _, v := range values {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

func recoverThinkResponseFromRaw(raw string, toolNames []string) (ThinkResponse, bool) {
	call, ok := recoverTextualToolCall(raw, toolNames)
	if !ok {
		answer := strings.TrimSpace(stripMarkdownFence(raw))
		if answer == "" || looksLikeMalformedControlPayload(answer) || isPrematureFinalAnswer(answer) || !looksLikeSubstantivePlainAnswer(answer) {
			return ThinkResponse{}, false
		}
		return ThinkResponse{
			Thought:     "Recovered plain-text final answer.",
			IsDone:      true,
			FinalAnswer: answer,
		}, true
	}
	thought := strings.TrimSpace(raw)
	if idx := strings.LastIndex(strings.ToLower(thought), "action:"); idx >= 0 {
		thought = strings.TrimSpace(thought[:idx])
	}
	thought = strings.TrimPrefix(thought, "Thought:")
	thought = strings.TrimSpace(thought)
	if thought == "" {
		thought = "Recovered legacy ReAct tool action."
	}
	return ThinkResponse{
		Thought: thought,
		IsDone:  false,
		Action:  call,
	}, true
}

func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

func looksLikeMalformedControlPayload(s string) bool {
	trimmed := strings.TrimSpace(s)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return true
	}
	for _, marker := range []string{"\"thought\"", "\"is_done\"", "\"action\"", "tool_name", "action input:", "action:"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeSubstantivePlainAnswer(s string) bool {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 24 {
		return false
	}
	if strings.Contains(trimmed, "\n") || strings.Contains(trimmed, "|") {
		return true
	}
	if strings.Contains(trimmed, ".") || strings.Contains(trimmed, "!") || strings.Contains(trimmed, "?") || strings.Contains(trimmed, ":") {
		return true
	}
	return false
}

func parseLegacyMapArgs(raw string) (map[string]any, bool) {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "map["), "]"))
	if body == "" {
		return map[string]any{}, true
	}

	// Common model fallback form for a single free-form argument:
	//   python_eval(map[code:import os
	//   print("hi")])
	// strings.Fields would destroy the code. Preserve everything after code:.
	if strings.HasPrefix(body, "code:") {
		return map[string]any{"code": strings.TrimSpace(strings.TrimPrefix(body, "code:"))}, true
	}

	// Go's fmt prints maps as map[k:v]. Some models copy that shape and values
	// may contain spaces, especially for file writes. Split only on known key
	// labels so values can remain free-form.
	if parsed, ok := parseKnownLegacyMap(body, []string{
		"path", "content", "queue", "item", "ttl", "to", "channel", "text", "message",
		"query", "kb", "title", "summary", "topic_tag", "source_url", "file_path",
		"source_channel", "timestamp", "status",
	}); ok {
		return parsed, true
	}

	args := map[string]any{}
	fields := strings.Fields(body)
	for _, f := range fields {
		k, v, ok := strings.Cut(f, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, false
		}
		args[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return args, true
}

func parseKnownLegacyMap(body string, keys []string) (map[string]any, bool) {
	type hit struct {
		key string
		at  int
	}
	var hits []hit
	for _, key := range keys {
		prefix := key + ":"
		searchFrom := 0
		for {
			idx := strings.Index(body[searchFrom:], prefix)
			if idx < 0 {
				break
			}
			at := searchFrom + idx
			if at == 0 || body[at-1] == ' ' || body[at-1] == '\n' || body[at-1] == '\t' {
				hits = append(hits, hit{key: key, at: at})
			}
			searchFrom = at + len(prefix)
		}
	}
	if len(hits) == 0 {
		return nil, false
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].at < hits[j-1].at; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	args := map[string]any{}
	for i, h := range hits {
		start := h.at + len(h.key) + 1
		end := len(body)
		if i+1 < len(hits) {
			end = hits[i+1].at
		}
		value := strings.TrimSpace(body[start:end])
		if value == "" {
			continue
		}
		if (strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}")) ||
			(strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]")) {
			var decoded any
			if err := json.Unmarshal([]byte(value), &decoded); err == nil {
				args[h.key] = decoded
				continue
			}
		}
		args[h.key] = value
	}
	return args, len(args) > 0
}

func toolAllowed(name string, toolNames []string) bool {
	for _, n := range toolNames {
		if n == name {
			return true
		}
	}
	return false
}

func isPrematureFinalAnswer(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" {
		return false
	}
	// Explicit progress phrases — flagged anywhere in the text.
	for _, phrase := range []string{
		"proceeding to step",
		"proceeding to list",
		"proceeding to process",
		"proceeding to check",
		"starting daily processing",
		"checking for pending",
		"pending resources queue exists",
		"i need to ",
		"let me ",
		"i will ",
		"next i ",
		"now i ",
		"about to ",
	} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	// Progress/intent OPENERS: a preamble *begins* by announcing what it is
	// about to do ("I'll scout the routes…", "Searching for fares…"). Anchored
	// to the start and gated to a short, single-block note so a real, structured
	// report that merely mentions these words later is never misflagged. This
	// catches contractions and gerund openers the phrase list above misses.
	if len(s) <= 400 && !strings.Contains(s, "\n") {
		for _, opener := range []string{
			// future-intent openers (contractions the phrase list above misses)
			"i'll ", "we'll ", "i'm going to ", "i am going to ", "i'm about to ",
			"i plan to ", "i shall ", "let's ", "let me now ", "first, i", "first i ",
			"next, i", "now i'll", "i'm now ", "going to ",
			// intent gerunds — anchored AND followed by a target preposition so a
			// results statement ("Searching returned 20 results…") is not flagged.
			"searching for ", "looking for ", "scouting for ", "scouting the ",
			"fetching the ", "retrieving the ", "gathering the ", "compiling the ",
		} {
			if strings.HasPrefix(s, opener) {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ─── Plan-Execute ─────────────────────────────────────────────────────────────

// planExecuteStrategy decomposes the task with one Plan() call, executes the
// planned steps in order with dependency gating, then reflects. Planning
// failure falls back to ReAct.
type planExecuteStrategy struct{}

func (planExecuteStrategy) Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse) {
	plan, err := env.LLM.Plan(ctx, env.Config.SystemPrompt, taskInput, env.Config.MaxPlanSteps)
	if err != nil || planHasUnavailableTool(plan, env.Config.ToolNames) {
		// Planning failed — fall back to ReAct.
		return reactStrategy{}.Run(ctx, env, taskInput)
	}

	completedIDs := map[string]bool{}
	var steps []Step

	for _, ps := range plan.Steps {
		if ctx.Err() != nil {
			break
		}

		// Dependency ordering: skip steps whose dependencies haven't completed.
		// Because the plan is already ordered, an unmet dependency means an
		// upstream step failed — we skip the dependent step gracefully.
		depsOK := true
		for _, dep := range ps.DependsOn {
			if !completedIDs[dep] {
				depsOK = false
				break
			}
		}
		if !depsOK {
			steps = append(steps, Step{
				ID:      ps.ID,
				Thought: ps.Description,
				Obs: Observation{
					Content: fmt.Sprintf("skipped: dependency %v not completed", ps.DependsOn),
				},
			})
			continue
		}

		stepCtx, cancel := context.WithTimeout(ctx, env.Config.StepTimeout)
		stepStart := time.Now()

		call := ToolCall{
			Tool:  ps.Tool,
			Input: map[string]string{"task": ps.Description},
		}
		obs := env.Tools.Execute(stepCtx, call)
		obs = boundObservation(obs)

		steps = append(steps, Step{
			ID:       ps.ID,
			Thought:  ps.Description,
			Action:   call,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		if !isToolFailure(obs) {
			completedIDs[ps.ID] = true
		}
		cancel()
	}

	resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: env.Config.SystemPrompt,
		OutputFormat: env.Config.OutputFormat,
	})
	if isPrematureFinalAnswer(resp.Output) && planExecutionHadIssue(steps) {
		resp.Output = planExecuteFallbackOutput(steps)
	} else if strings.TrimSpace(resp.Output) == "" || isPrematureFinalAnswer(resp.Output) {
		if obs := lastUsefulObservation(steps); obs != "" {
			resp.Output = obs
		} else {
			resp.Output = planExecuteFallbackOutput(steps)
		}
	}
	return steps, resp
}

func planExecuteFallbackOutput(steps []Step) string {
	if len(steps) == 0 {
		return "The run could not produce a plan to execute. Retry with a clearer request or choose a ReAct agent for open-ended tool use."
	}
	failed := 0
	skipped := 0
	for _, step := range steps {
		if isToolFailure(step.Obs) {
			failed++
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(step.Obs.Content)), "skipped:") {
			skipped++
		}
	}
	if failed > 0 || skipped > 0 {
		return fmt.Sprintf("The plan could not fully complete: %d step(s) failed and %d dependent step(s) were skipped. Open the run trace to inspect the failed step, then fix the tool input, credential, or workflow dependency.", failed, skipped)
	}
	return "The plan executed but the model did not produce a final answer. Open the run trace to inspect the completed steps."
}

func planExecutionHadIssue(steps []Step) bool {
	for _, step := range steps {
		if isToolFailure(step.Obs) {
			return true
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(step.Obs.Content)), "skipped:") {
			return true
		}
	}
	return false
}

func planHasUnavailableTool(plan Plan, toolNames []string) bool {
	if len(plan.Steps) == 0 {
		return true
	}
	if len(toolNames) == 0 {
		return false
	}
	for _, step := range plan.Steps {
		if strings.TrimSpace(step.Tool) == "" || !toolAllowed(step.Tool, toolNames) {
			return true
		}
	}
	return false
}
