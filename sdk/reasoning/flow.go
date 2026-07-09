package reasoning

// Flow types (Story E25) — declarative cyclic graphs. A FlowSpec is the
// graph form of a workflow: nodes perform work (tool / agent call) or
// branch, edges carry predicates and bounded-cycle budgets. Hosts compile
// SOUL.yaml's workflow block into this shape; the "flow" strategy and the
// checkpointing workflow executor both consume it.
//
// Compatibility: append-only fields, zero-value compatible.

// Flow node kinds.
const (
	FlowNodeTool   = "tool"   // run a tool (Tool + Input template)
	FlowNodeAgent  = "agent"  // invoke a peer agent (Agent = agent id)
	FlowNodeBranch = "branch" // no action; exists to fan edges out
	FlowNodePython = "python" // run inline Python (Code) or a deployed python tool (Tool)
	FlowNodeLLM    = "llm"    // run a constrained LLM transform/extraction step
	// FlowNodeTrigger and FlowNodeExit are STRUCTURAL endpoint blocks (Studio
	// visual authoring): a trigger marks where the flow starts (its Params carry
	// {kind: cron|http|channel, config}); an exit marks where it ends and how the
	// result leaves (Params carry {route: http|channel|console, config}). Both are
	// no-ops at run time — like a branch, they perform no action and just pass
	// control through — so they round-trip and validate without touching the
	// execution engine.
	FlowNodeTrigger = "trigger"
	FlowNodeExit    = "exit"
)

// IsStructuralKind reports whether a node kind performs NO runtime action and
// exists only to route/anchor the graph (branch, trigger, exit). The flow engine
// skips execution for these and only follows their edges.
func IsStructuralKind(kind string) bool {
	return kind == FlowNodeBranch || kind == FlowNodeTrigger || kind == FlowNodeExit
}

// FlowPort is a declared, named connection point on a node (Story S0.3).
// All fields are optional and purely descriptive — a node with no declared
// ports keeps today's single implicit input/output. Name identifies the
// port for edge wiring (FlowEdge.FromPort / ToPort); Type is an optional
// type hint (e.g. "string", "json") for tooling/validation; Label is an
// optional human-readable display name for editors.
type FlowPort struct {
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
	Type  string `yaml:"type,omitempty" json:"type,omitempty"`
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	// Field optionally decouples a port's wire name from the data it carries
	// (Story S0.3 runtime resolution). On an OUTPUT port it is the (optionally
	// dotted) path into the producing node's result that the port exposes — e.g.
	// a port named "notebook_id" with Field "notebook.id" carries result.notebook.id.
	// On an INPUT port it is the argument KEY the wired value is bound to in the
	// node's assembled input object, when that key should differ from the port
	// Name. Empty = use Name (the common case: port name == result field == arg
	// key). Purely declarative; the runtime reads it when assembling wired inputs.
	Field string `yaml:"field,omitempty" json:"field,omitempty"`
}

// FlowNode is one vertex of the graph.
type FlowNode struct {
	// ID is unique within the flow; checkpoint keys derive from it.
	ID string `yaml:"id" json:"id"`
	// Kind is tool | agent | branch (default tool when Tool is set,
	// branch when neither Tool nor Agent is set).
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Tool names the tool to invoke (kind=tool).
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`
	// Agent names a peer agent to invoke as agent__<id> (kind=agent).
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`
	// Description is a short, concrete human-readable line of exactly what this
	// node does (Studio shows it under the node on the canvas and in the
	// inspector). Purely descriptive; ignored by execution.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Intent is the user's plain-language description of what this node should do
	// (Studio "describe this step"). It is the source the per-node compiler turns
	// into concrete config (tool+args / mcp / skill / agent / python). Persisted
	// so a node is always re-editable as a prompt and a generated node round-trips
	// identically to a hand-built one (Phase C parity). Ignored by execution.
	Intent string `yaml:"intent,omitempty" json:"intent,omitempty"`
	// Code is the inline Python source for a kind=python node (Studio "Custom
	// Python" block). When set, the runtime executes it in the sandboxed Python
	// executor (process-per-call); inputs arrive as a JSON `inputs` payload and
	// the node's printed/returned value becomes its Output. Empty for a
	// python node that instead references a deployed tool via Tool.
	Code string `yaml:"code,omitempty" json:"code,omitempty"`
	// Requires lists the capabilities a kind=python node needs, inferred from
	// its Code by the Studio classifier (internal/studio/codeclass): a subset of
	// {"system","network"}. Empty = ReadOnly (inside the default guardrails).
	// Drives the per-case consent model; never widens what the runtime grants.
	Requires []string `yaml:"requires,omitempty" json:"requires,omitempty"`
	// Consent is the per-case grant recorded for a beyond-guardrail kind=python
	// node (Studio §13). It is bound to the exact code via Hash; the runtime
	// REFUSES to execute the node unless this stamp is present, its Hash matches
	// the current Code, and it covers the code's required capabilities. nil =
	// no grant — valid only for ReadOnly code. Pure data; the decision logic
	// lives in internal/studio/consent.
	Consent *FlowConsent `yaml:"consent,omitempty" json:"consent,omitempty"`
	// Input is a Go template producing the node's input from flow vars.
	Input string `yaml:"input,omitempty" json:"input,omitempty"`
	// Output names the flow var that stores this node's result.
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
	// OnError is retry | skip | abort (default abort).
	OnError string `yaml:"on_error,omitempty" json:"on_error,omitempty"`
	// Adaptive opts this node into runtime LLM salvage: when it fails or produces
	// a soft error (its output reports an error) because a real tool/API returned
	// an unexpected shape, the runtime asks the model to produce the node's
	// intended output from the actual input so the flow keeps running instead of
	// aborting. Bounded to one salvage attempt per node. Independent of the global
	// runtime.adaptive_nodes default. Applies to tool, python, and llm nodes.
	Adaptive bool `yaml:"adaptive,omitempty" json:"adaptive,omitempty"`
	// Timeout optionally overrides the global runtime.tool_timeout for THIS node's
	// execution (a Go duration string, e.g. "30s", "10m"). It lets a developer fix
	// a single slow-by-design block — e.g. a NotebookLM research/audio poll — by
	// raising just that block's budget, without weakening the global safety net for
	// every other node. Empty = use the global default. Invalid values are ignored
	// (and flagged by Studio validation). Applies to tool, agent, and python nodes.
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	// X is the visual layout X coordinate.
	X float64 `yaml:"x,omitempty" json:"x,omitempty"`
	// Y is the visual layout Y coordinate.
	Y float64 `yaml:"y,omitempty" json:"y,omitempty"`
	// Inputs declares named typed input ports (Story S0.3). Optional:
	// empty/nil = today's single implicit input port (unchanged behavior).
	Inputs []FlowPort `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	// Outputs declares named typed output ports (Story S0.3). Optional:
	// empty/nil = today's single implicit output port (unchanged behavior).
	Outputs []FlowPort `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	// Params is optional typed per-node configuration (Story S0.3) carried
	// alongside the node. nil = none. The flow runtime passes it through
	// untouched; it does not affect Input templating or execution order.
	Params map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
}

// FlowConsent records a user's per-case consent for a beyond-guardrail python
// node (system/network/dynamic code). It is content-bound: Hash is the
// first 12 hex chars of sha256(Code) at grant time, so editing the code voids
// the grant. Capabilities is the set the user approved. Scope is one of
// "run" | "workflow" | "until_revoked". Purely data — see internal/studio/consent.
type FlowConsent struct {
	Hash         string   `yaml:"hash" json:"hash"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Scope        string   `yaml:"scope,omitempty" json:"scope,omitempty"`
	GrantedAt    string   `yaml:"granted_at,omitempty" json:"granted_at,omitempty"`
	GrantedBy    string   `yaml:"granted_by,omitempty" json:"granted_by,omitempty"`
}

// FlowEdge is one directed edge. Edges from a node are evaluated IN ORDER;
// the first edge whose If renders truthy (and whose traversal budget is
// not exhausted) is taken. No eligible edge = the flow ends.
type FlowEdge struct {
	From string `yaml:"from" json:"from"`
	// To is the target node id; "end" (or empty) terminates the flow.
	To string `yaml:"to,omitempty" json:"to,omitempty"`
	// If is a Go template predicate over flow vars; empty/"true"/non-zero
	// output = take the edge, ""/"false"/"0" = don't.
	If string `yaml:"if,omitempty" json:"if,omitempty"`
	// MaxIterations bounds how many times THIS edge may be traversed per
	// run (bounded cycles). Default 1 — cycles are bounded unless a back
	// edge explicitly raises its budget.
	MaxIterations int `yaml:"max_iterations,omitempty" json:"max_iterations,omitempty"`
	// FromPort names a declared output port on the From node (Story S0.3).
	// Optional: "" = the implicit single output port (current behavior).
	// When set, it must match one of the From node's declared Outputs.
	FromPort string `yaml:"from_port,omitempty" json:"from_port,omitempty"`
	// ToPort names a declared input port on the To node (Story S0.3).
	// Optional: "" = the implicit single input port (current behavior).
	// When set, it must match one of the To node's declared Inputs.
	ToPort string `yaml:"to_port,omitempty" json:"to_port,omitempty"`
}

// FlowSpec is the whole graph.
type FlowSpec struct {
	Nodes []FlowNode `yaml:"nodes" json:"nodes"`
	Edges []FlowEdge `yaml:"edges,omitempty" json:"edges,omitempty"`
	// Entry is the starting node id (default: first node).
	Entry string `yaml:"entry,omitempty" json:"entry,omitempty"`
	// Output is the id of the node whose result becomes the flow's final output
	// (delivered to channels). Empty = the last node executed (default).
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
	// MaxNodeExecutions is the global safety budget across the whole run
	// (default 100). Exceeding it aborts the flow.
	MaxNodeExecutions int `yaml:"max_node_executions,omitempty" json:"max_node_executions,omitempty"`
}
