package agent

// WorkflowSpec is declared under the `workflow:` key in SOUL.yaml.
type WorkflowSpec struct {
	Steps []StepSpec `yaml:"steps" json:"steps"`
}

// StepSpec describes one step in a workflow DAG.
type StepSpec struct {
	ID      string `yaml:"id"       json:"id"`       // unique within workflow; used as checkpoint key
	Tool    string `yaml:"tool"     json:"tool"`     // tool name to invoke
	Prompt  string `yaml:"prompt"   json:"prompt"`   // optional LLM prompt for this step
	If      string `yaml:"if"       json:"if"`       // Go template condition; skip step if evaluates to falsy
	OnError string `yaml:"on_error" json:"on_error"` // "retry" | "skip" | "abort" (default: "abort")
	Input   string `yaml:"input"    json:"input"`    // Go template producing JSON input for the tool
	Output  string `yaml:"output"   json:"output"`   // variable name to store this step's JSON output
}
