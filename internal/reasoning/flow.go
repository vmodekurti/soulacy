// flow.go — Story E25: declarative cyclic flow graphs.
//
// CompileFlow validates a sdk/reasoning.FlowSpec; RunFlow walks the graph:
// nodes run through an injected runner (the engine bridges this to RunTool;
// the registered "flow" strategy bridges it to env.Tools), edges are
// evaluated in declaration order with Go-template predicates over the flow
// vars, and EVERY edge carries a traversal budget (default 1) so cycles
// terminate by construction. A global node-execution budget backstops
// pathological graphs. FlowHooks let the runtime checkpoint each node visit
// (visit-indexed keys) so resume-after-crash replays completed work instead
// of re-running it.
package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// DefaultFlowBudget is the global node-execution ceiling when the spec
// doesn't set MaxNodeExecutions.
const DefaultFlowBudget = 100

// FlowGraph is a compiled, validated flow.
type FlowGraph struct {
	spec  sdkr.FlowSpec
	nodes map[string]sdkr.FlowNode
	out   map[string][]int // node id → indexes into spec.Edges, declaration order
	entry string
}

// Node returns the compiled node by id (zero value if unknown).
func (g *FlowGraph) Node(id string) sdkr.FlowNode { return g.nodes[id] }

// Entry returns the entry node id.
func (g *FlowGraph) Entry() string { return g.entry }

// Spec returns the underlying spec (for GUI rendering).
func (g *FlowGraph) Spec() sdkr.FlowSpec { return g.spec }

// CompileFlow validates the spec and returns an executable graph.
func CompileFlow(spec sdkr.FlowSpec) (*FlowGraph, error) {
	if len(spec.Nodes) == 0 {
		return nil, fmt.Errorf("flow: no nodes declared")
	}
	nodes := make(map[string]sdkr.FlowNode, len(spec.Nodes))
	for i, n := range spec.Nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("flow: node %d has no id", i)
		}
		if _, dup := nodes[n.ID]; dup {
			return nil, fmt.Errorf("flow: duplicate node id %q", n.ID)
		}
		// Kind inference: tool set → tool, agent set → agent, code set → python,
		// none of those → branch.
		if n.Kind == "" {
			switch {
			case n.Tool != "":
				n.Kind = sdkr.FlowNodeTool
			case n.Agent != "":
				n.Kind = sdkr.FlowNodeAgent
			case n.Code != "":
				n.Kind = sdkr.FlowNodePython
			default:
				n.Kind = sdkr.FlowNodeBranch
			}
		}
		// Tolerate common entry/exit synonyms a builder model sometimes emits
		// ("start"/"entry"/"begin" for the first node, "end"/"finish"/"done" for
		// the last). These are structural passthroughs — the real entry is the
		// `entry` field — so map them rather than hard-failing the whole flow.
		switch strings.ToLower(strings.TrimSpace(n.Kind)) {
		case "start", "entry", "begin", "receive", "input_node":
			n.Kind = sdkr.FlowNodeTrigger
		case "end", "finish", "done", "output_node":
			n.Kind = sdkr.FlowNodeExit
		}
		switch n.Kind {
		case sdkr.FlowNodeTool:
			if n.Tool == "" {
				return nil, fmt.Errorf("flow: node %q is kind=tool but names no tool", n.ID)
			}
		case sdkr.FlowNodeAgent:
			if n.Agent == "" {
				return nil, fmt.Errorf("flow: node %q is kind=agent but names no agent", n.ID)
			}
		case sdkr.FlowNodePython:
			// A python node must carry either inline Code or reference a
			// deployed python tool by name.
			if n.Code == "" && n.Tool == "" {
				return nil, fmt.Errorf("flow: node %q is kind=python but has neither inline code nor a tool", n.ID)
			}
		case sdkr.FlowNodeLLM:
			// LLM transform nodes use Input plus params.system/params.response_format
			// at runtime; no extra required field beyond the node itself.
		case sdkr.FlowNodeBranch, sdkr.FlowNodeTrigger, sdkr.FlowNodeExit:
			// Structural endpoint/routing nodes: no action, nothing to validate.
		default:
			return nil, fmt.Errorf("flow: node %q has unknown kind %q", n.ID, n.Kind)
		}
		switch n.OnError {
		case "", "abort", "skip", "retry":
		default:
			return nil, fmt.Errorf("flow: node %q has unknown on_error %q", n.ID, n.OnError)
		}
		nodes[n.ID] = n
	}

	out := map[string][]int{}
	for i, e := range spec.Edges {
		if _, ok := nodes[e.From]; !ok {
			return nil, fmt.Errorf("flow: edge %d from unknown node %q", i, e.From)
		}
		if !flowEdgeTerminal(e.To) {
			if _, ok := nodes[e.To]; !ok {
				return nil, fmt.Errorf("flow: edge %d to unknown node %q", i, e.To)
			}
		}
		// Typed ports (Story S0.3): empty FromPort/ToPort = implicit single
		// port (unchanged). When a port is named it must exist among the
		// referenced node's declared ports.
		if e.FromPort != "" && !flowHasPort(nodes[e.From].Outputs, e.FromPort) {
			return nil, fmt.Errorf("flow: edge %d from_port %q not declared on node %q outputs", i, e.FromPort, e.From)
		}
		if e.ToPort != "" && !flowEdgeTerminal(e.To) && !flowHasPort(nodes[e.To].Inputs, e.ToPort) {
			return nil, fmt.Errorf("flow: edge %d to_port %q not declared on node %q inputs", i, e.ToPort, e.To)
		}
		out[e.From] = append(out[e.From], i)
	}

	entry := spec.Entry
	if entry == "" {
		entry = spec.Nodes[0].ID
	}
	if _, ok := nodes[entry]; !ok {
		return nil, fmt.Errorf("flow: entry node %q does not exist", entry)
	}

	return &FlowGraph{spec: spec, nodes: nodes, out: out, entry: entry}, nil
}

func flowEdgeTerminal(to string) bool { return to == "" || to == "end" }

// flowHasPort reports whether ports declares one named name (Story S0.3).
func flowHasPort(ports []sdkr.FlowPort, name string) bool {
	for _, p := range ports {
		if p.Name == name {
			return true
		}
	}
	return false
}

// FlowRunNode executes one node's action with its rendered input and
// returns the node's JSON result. Branch nodes are never passed to it.
type FlowRunNode func(ctx context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error)

// FlowHooks are optional observation/persistence seams. visitKey is
// "<nodeID>#<visit>" — visit counts per node from 1 — so cyclic re-visits
// checkpoint under distinct keys and resume replays them in order.
type FlowHooks struct {
	// Restore returns the persisted state for a visit that already
	// completed in a previous run (resume). ok=false = execute normally.
	Restore func(visitKey string) (state json.RawMessage, ok bool)
	// Started fires before a node visit executes.
	Started func(visitKey string, node sdkr.FlowNode)
	// Completed fires after a visit succeeds (or is skipped on error),
	// with the state that entered the vars.
	Completed func(visitKey string, state json.RawMessage)
	// Failed fires when a visit aborts the flow.
	Failed func(visitKey string, err error)
	// Observe fires after every executed (non-branch) node visit with the full
	// per-node record — rendered input, output, error, and wall-clock duration —
	// so the runtime can build a legible per-block run trace. It fires on
	// success, on a skipped error, and on an aborting error (before Failed).
	// It never fires for a restored (resumed) visit. Purely observational.
	Observe func(rec FlowNodeRun)
}

// FlowNodeRun is one executed node's record in a run trace (Story S0.3 /
// per-block logging). It captures exactly what a block received, produced, how
// long it took, and whether it errored — the data a non-technical user needs to
// see WHERE a run went wrong, without reading templates or logs.
type FlowNodeRun struct {
	VisitKey   string          `json:"visitKey"`
	NodeID     string          `json:"nodeId"`
	Kind       string          `json:"kind"`
	Input      string          `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"durationMs"`
	StartedAt  time.Time       `json:"startedAt"`
	// WiredPorts is true when this node's input was assembled from typed port
	// wires (template-free handoff) rather than a Go-template input.
	WiredPorts bool `json:"wiredPorts,omitempty"`
}

// RunFlow walks the compiled graph. vars seeds the template namespace
// (callers typically set "trigger"); node outputs land under their Output
// names. Returns the last executed node's result.
func RunFlow(ctx context.Context, g *FlowGraph, vars map[string]any, run FlowRunNode, hooks FlowHooks) (json.RawMessage, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	budget := g.spec.MaxNodeExecutions
	if budget <= 0 {
		budget = DefaultFlowBudget
	}

	visits := map[string]int{} // node id → times visited
	traversed := map[int]int{} // edge index → times traversed
	executions := 0
	var lastResult json.RawMessage

	current := g.entry
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("flow: cancelled at node %q: %w", current, err)
		}
		node := g.nodes[current]
		visits[current]++
		visitKey := fmt.Sprintf("%s#%d", current, visits[current])

		if !sdkr.IsStructuralKind(node.Kind) {
			executions++
			if executions > budget {
				err := fmt.Errorf("flow: node-execution budget exhausted (%d) at %q — check cycle bounds", budget, current)
				if hooks.Failed != nil {
					hooks.Failed(visitKey, err)
				}
				return nil, err
			}

			restored := false
			if hooks.Restore != nil {
				if state, ok := hooks.Restore(visitKey); ok {
					applyFlowResult(vars, node, state)
					if state != nil {
						lastResult = state
					}
					restored = true
				}
			}

			if !restored {
				if hooks.Started != nil {
					hooks.Started(visitKey, node)
				}

				// Typed-port handoffs (Story S0.3 runtime resolution): if any
				// incoming edge declares a to_port, the node's input is assembled
				// from the wired upstream output ports — no Go template. This is the
				// template-free handoff path that removes the {{ .x.id }} bug class.
				overlay, hasPorts := resolvePortInputs(g, current, vars)

				rendered := ""
				switch {
				case node.Input != "":
					var rerr error
					rendered, rerr = renderTemplate(node.Input, vars)
					if rerr != nil {
						err := fmt.Errorf("flow: node %q: render input: %w", current, rerr)
						if hooks.Failed != nil {
							hooks.Failed(visitKey, err)
						}
						return nil, err
					}
				case node.Kind == sdkr.FlowNodePython && !hasPorts:
					// A python node with no explicit Input AND no wired ports receives
					// ALL flow vars as a JSON `inputs` dict (keyed by each prior node's
					// output var), so its run(inputs) sees upstream outputs without
					// manual templating. When ports ARE wired the node instead receives
					// exactly its declared inputs (assembled below).
					if b, jerr := json.Marshal(vars); jerr == nil {
						rendered = string(b)
					} else {
						rendered = "{}"
					}
				}

				// Overlay the wired port values onto the static base (constants the
				// node carries in Input as a JSON object). The wires win.
				if hasPorts {
					base := map[string]any{}
					if s := strings.TrimSpace(rendered); s != "" {
						// Ignore a parse error: a non-object base (prose) is dropped in
						// favor of the structured wired inputs.
						_ = json.Unmarshal([]byte(s), &base)
					}
					for k, v := range overlay {
						base[k] = v
					}
					if b, jerr := json.Marshal(base); jerr == nil {
						rendered = string(b)
					}
				}

				start := time.Now()
				result, err := run(ctx, node, rendered)
				if err != nil && node.OnError == "retry" {
					result, err = run(ctx, node, rendered)
				}
				// Record the per-node run (success, skip, or abort) before deciding
				// the flow's fate, so the trace always shows what each block did.
				if hooks.Observe != nil {
					rec := FlowNodeRun{
						VisitKey:   visitKey,
						NodeID:     current,
						Kind:       node.Kind,
						Input:      rendered,
						Output:     result,
						DurationMS: time.Since(start).Milliseconds(),
						StartedAt:  start.UTC(),
						WiredPorts: hasPorts,
					}
					if err != nil {
						rec.Error = err.Error()
					}
					hooks.Observe(rec)
				}
				if err != nil {
					if node.OnError == "skip" {
						result = nil
					} else {
						ferr := fmt.Errorf("flow: node %q: %w", current, err)
						if hooks.Failed != nil {
							hooks.Failed(visitKey, ferr)
						}
						return nil, ferr
					}
				}
				applyFlowResult(vars, node, result)
				if result != nil {
					lastResult = result
				}
				if hooks.Completed != nil {
					hooks.Completed(visitKey, result)
				}
			}
		}

		// Pick the next edge: declaration order, first whose predicate is
		// truthy AND whose traversal budget (default 1) isn't exhausted.
		next := ""
		found := false
		for _, ei := range g.out[current] {
			e := g.spec.Edges[ei]
			maxIter := e.MaxIterations
			if maxIter <= 0 {
				maxIter = 1
			}
			if traversed[ei] >= maxIter {
				continue
			}
			if e.If != "" {
				cond, err := renderTemplate(e.If, vars)
				if err != nil {
					return nil, fmt.Errorf("flow: edge %q→%q: render predicate: %w", e.From, e.To, err)
				}
				cond = strings.TrimSpace(cond)
				if cond == "" || cond == "false" || cond == "0" || cond == "<no value>" {
					continue
				}
			}
			traversed[ei]++
			next = e.To
			found = true
			break
		}
		if !found || flowEdgeTerminal(next) {
			return lastResult, nil
		}
		current = next
	}
}

// resolvePortInputs assembles a node's input from its incoming WIRED edges
// (Story S0.3 runtime resolution). For every edge whose To is this node and that
// declares a ToPort, it reads the producing node's stored output var, extracts
// the field the FromPort exposes, and binds it under the consumer's input-port
// key. The result is the structured, template-free input the runtime overlays
// onto the node's static base. ok=false when the node has no wired inputs — the
// caller then keeps today's template/all-vars behavior unchanged.
//
// Forgiving by design: a producer that hasn't run yet (value absent) binds the
// zero value rather than erroring, and a FromPort whose field can't be walked
// falls back to the whole upstream value — so a slightly-off wire degrades
// gracefully instead of aborting the run.
func resolvePortInputs(g *FlowGraph, nodeID string, vars map[string]any) (map[string]any, bool) {
	consumer, ok := g.nodes[nodeID]
	if !ok {
		return nil, false
	}
	overlay := map[string]any{}
	used := false
	for _, e := range g.spec.Edges {
		if e.To != nodeID || e.ToPort == "" {
			continue
		}
		src, ok := g.nodes[e.From]
		if !ok {
			continue
		}
		var srcVal any
		if src.Output != "" {
			srcVal = vars[src.Output]
		}
		// Which value does the from_port expose?
		//   - no from_port            → the whole producer output
		//   - port has explicit Field → strict dotted path into the output
		//   - else (port NAME)        → the output's same-named field IF present,
		//                               otherwise the WHOLE output (a generic port
		//                               name like "result"/"output" addresses the
		//                               whole thing; a specific one like "id"
		//                               addresses that field).
		var val any
		switch {
		case e.FromPort == "":
			val = srcVal
		default:
			if op := findPort(src.Outputs, e.FromPort); op != nil && op.Field != "" {
				val = extractField(srcVal, op.Field)
			} else {
				val = extractNamedField(srcVal, e.FromPort)
			}
		}
		// Bind under the consumer's input-port key (Field override or port name).
		key := e.ToPort
		if ip := findPort(consumer.Inputs, e.ToPort); ip != nil && ip.Field != "" {
			key = ip.Field
		}
		overlay[key] = val
		used = true
	}
	return overlay, used
}

// findPort returns the named port (or nil) from a port list.
func findPort(ports []sdkr.FlowPort, name string) *sdkr.FlowPort {
	for i := range ports {
		if ports[i].Name == name {
			return &ports[i]
		}
	}
	return nil
}

// extractNamedField resolves a port-NAME reference against a producer output:
// when the output is an object that HAS the key, it returns that field; in every
// other case (output isn't an object, or has no such key) it returns the whole
// output. This makes a generic port name ("result", "output") carry the whole
// value while a specific one ("id", "url") carries just that field — without the
// author having to declare an explicit Field.
func extractNamedField(v any, name string) any {
	if m, ok := v.(map[string]any); ok {
		if val, present := m[name]; present {
			return val
		}
	}
	return v
}

// extractField walks a dotted path into a decoded JSON value and returns the
// addressed leaf. An empty path returns the whole value. If a segment can't be
// walked (the value isn't an object at that depth), it returns what it reached
// so far — so wiring the whole of a scalar output via a named port still yields
// the scalar instead of nil.
func extractField(v any, path string) any {
	if strings.TrimSpace(path) == "" {
		return v
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return cur
		}
		cur = m[seg]
	}
	return cur
}

// applyFlowResult stores a node result under its Output var (parsed JSON
// when possible, raw string otherwise) — same semantics as workflow steps.
func applyFlowResult(vars map[string]any, node sdkr.FlowNode, result json.RawMessage) {
	if node.Output == "" || result == nil {
		return
	}
	var v any
	if err := json.Unmarshal(result, &v); err == nil {
		vars[node.Output] = v
	} else {
		vars[node.Output] = string(result)
	}
}
