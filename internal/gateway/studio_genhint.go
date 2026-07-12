// studio_genhint.go — turn a Studio generation failure into a plain-language,
// actionable message instead of a raw parser error.
//
// Studio's builder prompt (JSON schema + the full tool/MCP/agent catalog) is the
// hardest structured-output task in the product. When a weak builder model can't
// hold it, the failure surfaces as `studio: parse draft: invalid character '{'…`
// or as a draft of empty placeholder steps — neither of which tells the operator
// what to actually do. This names the builder model that failed and the two
// fixes that resolve it in practice.

package gateway

import (
	"fmt"
	"strings"
)

// studioGenerationHint wraps a generation error with the builder model that
// produced it and concrete remedies. Non-generation errors pass through
// unchanged so real bugs aren't masked by advice.
func (s *Server) studioGenerationHint(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	low := strings.ToLower(raw)

	// Only advise on the "the model's output was unusable" family.
	badOutput := strings.Contains(low, "parse draft") ||
		strings.Contains(low, "no json object found") ||
		strings.Contains(low, "model output:") ||
		strings.Contains(low, "did not produce a usable workflow") ||
		strings.Contains(low, "compiled flow is invalid")
	if !badOutput {
		return raw
	}

	// A TRUNCATED response is a length problem, not a model-capability problem —
	// advise on the real cause and don't send the user chasing a bigger model.
	if strings.Contains(low, "cut off") || strings.Contains(low, "[truncated") {
		return s.truncationHint(raw)
	}

	provider, model := s.studioProviderModel()
	who := strings.TrimSpace(provider + " / " + model)
	who = strings.Trim(who, "/ ")
	if who == "" {
		who = "the configured builder model"
	}

	var b strings.Builder
	b.WriteString("The Studio builder model (")
	b.WriteString(who)
	b.WriteString(") didn't return a usable workflow. ")
	b.WriteString(reasonTail(raw))
	b.WriteString(" Generating a workflow requires a model that can follow a strict JSON schema — it's the hardest structured-output task in Soulacy.\n\n")
	b.WriteString("Fixes:\n")
	b.WriteString("• Point Studio at a stronger builder model (llm.studio.provider / llm.studio.model). Your agents can keep running on the local model — only generation needs the stronger one.\n")
	if strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		b.WriteString("• On Ollama, make sure the context window is large enough for Studio's schema + tool catalog: " + s.numCtxAdvice(provider) + "\n")
	}
	return b.String()
}

// truncationHint handles the case where the model's JSON was CUT OFF. That's a
// budget problem, not a capability problem — a bigger model won't help, more
// room will.
func (s *Server) truncationHint(raw string) string {
	provider, model := s.studioProviderModel()
	who := strings.Trim(strings.TrimSpace(provider+" / "+model), "/ ")
	if who == "" {
		who = "the configured builder model"
	}
	var b strings.Builder
	b.WriteString("The Studio builder model (")
	b.WriteString(who)
	b.WriteString(") started a valid workflow but its response was CUT OFF before it finished. ")
	b.WriteString("This is a length limit, not a reasoning failure — a bigger model won't fix it, more room will.\n\n")
	b.WriteString("Fixes:\n")
	b.WriteString("• " + s.numCtxAdvice(provider) + "\n")
	b.WriteString("• Reduce what has to fit: trim the prompt, or install fewer tools/MCP servers (the whole catalog is sent to the builder).\n")
	b.WriteString("• Note: a model's advertised context is not always what it serves — some Ollama builds cap the served context below the requested num_ctx.\n\n")
	b.WriteString("Raw model output (for diagnosis):\n")
	b.WriteString(reasonTail(raw))
	if i := strings.Index(raw, "model output: "); i >= 0 {
		b.WriteString(strings.TrimSpace(raw[i+len("model output: "):]))
	}
	return b.String()
}

// numCtxAdvice reports the ACTUAL configured context window (never a guessed
// default) and what to do about it.
func (s *Server) numCtxAdvice(provider string) string {
	if !strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		return "Raise the model's output/context budget so the whole workflow JSON fits."
	}
	cur := ""
	if pc, ok := s.cfg.LLM.Providers[strings.TrimSpace(provider)]; ok && pc.Options != nil {
		if v, ok := pc.Options["num_ctx"]; ok {
			cur = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if cur == "" {
		return "Set the context window in Providers → Ollama → Context window (num_ctx) to 32768 or more (Ollama's default is only 4096, which truncates Studio's prompt)."
	}
	return "Your Ollama context window (num_ctx) is currently " + cur +
		". If the response is still being cut off, raise it further (and confirm the model/Ollama build actually serves that much context — some cap it lower)."
}

// reasonTail extracts the underlying cause as a short sentence.
func reasonTail(raw string) string {
	low := strings.ToLower(raw)
	switch {
	case strings.Contains(low, "did not produce a usable workflow"):
		// Already a plain-English reason from DegenerateReason — reuse its tail.
		if i := strings.Index(raw, ": "); i >= 0 {
			if j := strings.LastIndex(raw, ": "); j > i {
				return "It returned " + strings.TrimSpace(raw[j+2:]) + "."
			}
		}
		return "It returned placeholder steps rather than a real workflow."
	case strings.Contains(low, "parse draft"), strings.Contains(low, "no json object found"):
		return "Its output wasn't valid JSON (often a truncated or malformed response)."
	case strings.Contains(low, "compiled flow is invalid"):
		return "The steps it produced don't form a valid flow."
	}
	return ""
}
