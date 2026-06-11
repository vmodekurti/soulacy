// codegen.go — focused, in-framework code generation for ONE Custom Python
// node. The framework's OWN configured model (llm.studio → studioLLM) writes a
// complete `def run(inputs)` body from the node's description + the surrounding
// workflow context. This is distinct from a whole-workflow compile: it targets
// a single node, so the result is concrete and complete rather than a skeleton.
//
// No external service is involved — the same llm.Router the rest of the gateway
// uses authors the code, which the user can then review, run in the dry-run
// bench, and (for beyond-guardrail code) consent to.
package studio

import (
	"context"
	"fmt"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// CodegenRequest asks the framework to write one python node's code.
type CodegenRequest struct {
	NodeID      string `json:"nodeId"`
	Description string `json:"description"`
	Workflow    Draft  `json:"workflow"`
}

// GenerateNodeCode returns a complete `def run(inputs)` body for the requested
// node, authored by the framework's model. Returns an error if no model is
// configured.
func GenerateNodeCode(ctx context.Context, llm LLM, req CodegenRequest) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("studio: no model is configured for code generation")
	}
	out, err := llm.Complete(ctx, buildCodegenPrompt(req))
	if err != nil {
		return "", err
	}
	code := extractPythonCode(out)
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("studio: code generation returned nothing")
	}
	return code, nil
}

// buildCodegenPrompt grounds the model in the node's job and the data it will
// receive (the output vars of every other node), and pins the run(inputs)
// contract. It asks for code only.
func buildCodegenPrompt(req CodegenRequest) string {
	var b strings.Builder
	b.WriteString("You are the Soulacy Studio code writer. Write the COMPLETE Python for ONE workflow step.\n\n")
	b.WriteString("Output RULES:\n")
	b.WriteString("- Output ONLY Python source. No prose, no markdown, no code fences.\n")
	b.WriteString("- Define exactly one function: def run(inputs):\n")
	b.WriteString("- `inputs` is a dict of upstream node outputs keyed by each node's output variable. Read what you need, e.g. inputs.get(\"articles\").\n")
	b.WriteString("- RETURN the result (JSON-serialisable, or a string). Do not print it.\n")
	b.WriteString("- Write real, working code — never a stub, `pass`, TODO, or `return inputs`. Handle errors with clear exceptions.\n")
	b.WriteString("- Use only the Python standard library unless a dependency is truly required.\n\n")

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = "(no description given — infer from the node id and surrounding workflow)"
	}
	fmt.Fprintf(&b, "This step (node id %q) must do:\n%s\n\n", req.NodeID, desc)

	// List the data this node can read: the output var of every OTHER node.
	var avail []string
	for _, n := range req.Workflow.Flow.Nodes {
		if n.ID == req.NodeID {
			continue
		}
		out := strings.TrimSpace(n.Output)
		if out == "" {
			continue
		}
		label := describeNodeShort(n)
		avail = append(avail, fmt.Sprintf("  - inputs[%q]  (%s)", out, label))
	}
	if len(avail) > 0 {
		b.WriteString("Available inputs (upstream outputs you may read):\n")
		b.WriteString(strings.Join(avail, "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString("Write the def run(inputs) now.")
	return b.String()
}

// describeNodeShort is a one-phrase summary of a node for the codegen context.
func describeNodeShort(n sdkr.FlowNode) string {
	if d := strings.TrimSpace(n.Description); d != "" {
		return d
	}
	switch {
	case n.Tool != "":
		return "output of tool " + n.Tool
	case n.Agent != "":
		return "output of agent " + n.Agent
	default:
		return "output of " + n.ID
	}
}

// extractPythonCode strips a leading ```python / ``` fence and trailing ``` if
// the model wrapped its output despite instructions.
func extractPythonCode(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the first fence line.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
