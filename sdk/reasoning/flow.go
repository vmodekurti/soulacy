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
)

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
	// Input is a Go template producing the node's input from flow vars.
	Input string `yaml:"input,omitempty" json:"input,omitempty"`
	// Output names the flow var that stores this node's result.
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
	// OnError is retry | skip | abort (default abort).
	OnError string `yaml:"on_error,omitempty" json:"on_error,omitempty"`
	// X is the visual layout X coordinate.
	X float64 `yaml:"x,omitempty" json:"x,omitempty"`
	// Y is the visual layout Y coordinate.
	Y float64 `yaml:"y,omitempty" json:"y,omitempty"`
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
}

// FlowSpec is the whole graph.
type FlowSpec struct {
	Nodes []FlowNode `yaml:"nodes" json:"nodes"`
	Edges []FlowEdge `yaml:"edges,omitempty" json:"edges,omitempty"`
	// Entry is the starting node id (default: first node).
	Entry string `yaml:"entry,omitempty" json:"entry,omitempty"`
	// MaxNodeExecutions is the global safety budget across the whole run
	// (default 100). Exceeding it aborts the flow.
	MaxNodeExecutions int `yaml:"max_node_executions,omitempty" json:"max_node_executions,omitempty"`
}
