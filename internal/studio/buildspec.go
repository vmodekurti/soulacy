package studio

// buildspec.go — ST-01 "Intent To Build Spec".
//
// The trust problem this addresses: today a user types a paragraph, presses
// Generate, and a graph appears. If the graph is wrong they cannot tell whether
// Studio misunderstood the intent or built the misunderstanding correctly —
// those are different bugs with different fixes, and the UI collapses them.
//
// A BuildSpec sits between the two. It states, in the user's own vocabulary,
// exactly what Studio believes it was asked for: when this runs, what it reads,
// what it does, where the result goes, what it needs access to. The user
// confirms or corrects THAT, before a single node exists.
//
// Extraction is deliberately deterministic. A model may refine the prose (see
// Refine), but the structured reading of that prose is rule-based, so the same
// intent always yields the same spec and a user can learn what Studio keys on.
// The screens render this directly: "Studio understood" is a BuildSpec.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SpecStage is one processing step the user described, before it becomes nodes.
type SpecStage struct {
	// Name is a short label ("search sources", "generate audio").
	Name string `json:"name"`
	// Detail is the phrase from the intent this came from, so the user can see
	// WHY Studio believes this stage exists rather than trusting a summary.
	Detail string `json:"detail,omitempty"`
	// Parallel marks a stage the planner intends to fan out.
	Parallel bool `json:"parallel,omitempty"`
}

// SpecQuestion is a required detail the intent did not supply. It is a focused
// question rather than a validation error because at this point nothing is
// wrong — the user simply hasn't said yet.
type SpecQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Why      string `json:"why"`
	// Blocker marks a detail without which nothing can be built (as opposed to
	// one where a sensible default exists).
	Blocker bool `json:"blocker"`
	// Field names the spec field an answer populates, so the UI can bind an
	// inline control instead of re-parsing free text.
	Field string `json:"field,omitempty"`
}

// BuildSpec is Studio's structured reading of an intent.
type BuildSpec struct {
	// Intent is the prose this was read from. Always preserved and editable —
	// ST-01 requires the original prompt never be lost behind its refinement.
	Intent string `json:"intent"`

	Trigger      string      `json:"trigger,omitempty"`  // schedule | channel | webhook | manual
	Schedule     string      `json:"schedule,omitempty"` // cron, when trigger=schedule
	ScheduleText string      `json:"schedule_text,omitempty"`
	Inputs       []string    `json:"inputs,omitempty"`
	Stages       []SpecStage `json:"stages,omitempty"`
	Outputs      []string    `json:"outputs,omitempty"`
	Delivery     []string    `json:"delivery,omitempty"`
	Integrations []string    `json:"integrations,omitempty"`
	// Security is the access this will need, stated plainly ("reads the web",
	// "sends messages on your behalf") so consent is an informed decision.
	Security  []string       `json:"security,omitempty"`
	Questions []SpecQuestion `json:"questions,omitempty"`
}

// Ready reports whether the spec has everything required to generate.
func (s BuildSpec) Ready() bool {
	for _, q := range s.Questions {
		if q.Blocker {
			return false
		}
	}
	return len(s.Stages) > 0
}

// Blockers returns only the questions that prevent generation.
func (s BuildSpec) Blockers() []SpecQuestion {
	var out []SpecQuestion
	for _, q := range s.Questions {
		if q.Blocker {
			out = append(out, q)
		}
	}
	return out
}

var (
	reTimeOfDay  = regexp.MustCompile(`(?i)\b(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b`)
	reDomain     = regexp.MustCompile(`\b([a-z0-9][a-z0-9\-]{1,}\.(?:com|org|net|io|ai|co|dev|news|gov|edu))\b`)
	reEveryday   = regexp.MustCompile(`(?i)\b(?:every|each)\s+(weekday|day|morning|monday|tuesday|wednesday|thursday|friday|saturday|sunday|week|hour)\b`)
	reWhitespace = regexp.MustCompile(`\s+`)
	// A destination is a chat/channel id, an @handle, a #channel, or an email —
	// deliberately NOT "any digit", which a time of day satisfies.
	reDestination = regexp.MustCompile(`(?i)(chat\s*(id)?\s*[:#]?\s*-?\d{3,}|@[a-z0-9_.]{2,}|#[a-z0-9\-_]{2,}|-100\d+|\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b)`)
)

// ExtractBuildSpec reads an intent into a structured spec. Pure and
// deterministic: the same text always yields the same spec, so a user can learn
// what Studio keys on instead of guessing at a model's mood.
func ExtractBuildSpec(intent string) BuildSpec {
	spec := BuildSpec{Intent: strings.TrimSpace(intent)}
	low := strings.ToLower(spec.Intent)
	if strings.TrimSpace(low) == "" {
		spec.Questions = append(spec.Questions, SpecQuestion{
			ID: "intent", Question: "What should this agent do?",
			Why: "Nothing can be built without a description of the job.", Blocker: true, Field: "intent",
		})
		return spec
	}

	spec.Trigger, spec.Schedule, spec.ScheduleText = extractTrigger(low)
	spec.Inputs = extractInputs(spec.Intent, low)
	spec.Stages = extractStages(low)
	spec.Outputs = extractOutputs(low)
	spec.Delivery = extractDelivery(low)
	spec.Integrations = extractIntegrations(low)
	spec.Security = deriveSecurity(spec)
	spec.Questions = deriveQuestions(spec, low)
	return spec
}

func extractTrigger(low string) (kind, cron, text string) {
	switch {
	case reEveryday.MatchString(low) || strings.Contains(low, "schedule") ||
		strings.Contains(low, "daily") || strings.Contains(low, "weekly"):
		kind = "schedule"
	case strings.Contains(low, "when someone") || strings.Contains(low, "on message") ||
		strings.Contains(low, "responds to") || strings.Contains(low, "answers questions"):
		return "channel", "", "when a message arrives"
	case strings.Contains(low, "webhook") || strings.Contains(low, "http post"):
		return "webhook", "", "on an inbound HTTP request"
	default:
		return "manual", "", "when you run it"
	}

	// Resolve the day set and the time of day into a real cron expression, so
	// the spec commits to something checkable rather than repeating the prose.
	days := "*"
	dayText := "every day"
	if m := reEveryday.FindStringSubmatch(low); m != nil {
		switch strings.ToLower(m[1]) {
		case "weekday":
			days, dayText = "1-5", "every weekday"
		case "monday":
			days, dayText = "1", "every Monday"
		case "tuesday":
			days, dayText = "2", "every Tuesday"
		case "wednesday":
			days, dayText = "3", "every Wednesday"
		case "thursday":
			days, dayText = "4", "every Thursday"
		case "friday":
			days, dayText = "5", "every Friday"
		case "saturday":
			days, dayText = "6", "every Saturday"
		case "sunday":
			days, dayText = "0", "every Sunday"
		}
	}
	hour, minute, haveTime := extractTimeOfDay(low)
	if !haveTime {
		return "schedule", "", dayText + " (time not specified)"
	}
	cron = fmt.Sprintf("%d %d * * %s", minute, hour, days)
	text = fmt.Sprintf("%s at %02d:%02d (local time)", dayText, hour, minute)
	return "schedule", cron, text
}

// extractTimeOfDay finds a wall-clock time, correctly handling the am/pm cases
// that a naive parser gets wrong (12am is hour 0; 12pm is hour 12).
func extractTimeOfDay(low string) (hour, minute int, ok bool) {
	for _, m := range reTimeOfDay.FindAllStringSubmatch(low, -1) {
		h := atoiSafe(m[1])
		if h < 0 || h > 24 {
			continue
		}
		mm := 0
		if m[2] != "" {
			mm = atoiSafe(m[2])
		}
		switch strings.ToLower(m[3]) {
		case "pm":
			if h < 12 {
				h += 12
			}
		case "am":
			if h == 12 {
				h = 0
			}
		case "":
			// A bare number is only a time when it was written like one
			// ("7:00"); a bare "5" in "top 5 stories" must not become 05:00.
			if m[2] == "" {
				continue
			}
		}
		if h >= 0 && h < 24 && mm >= 0 && mm < 60 {
			return h, mm, true
		}
	}
	return 0, 0, false
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func extractInputs(original, low string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			return
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	for _, m := range reDomain.FindAllStringSubmatch(strings.ToLower(original), -1) {
		add(m[1])
	}
	// An explicit "Sources: A, B, C" list names publications that are not
	// domains ("MIT Technology Review"). Reading only domains silently dropped
	// a third of what the user asked for — and the spec exists precisely so
	// that kind of omission is visible before anything is built.
	for _, item := range sourcesListItems(original) {
		add(item)
	}
	if len(out) == 0 {
		for phrase, label := range map[string]string{
			"the web":      "web search results",
			"web search":   "web search results",
			"my calendar":  "your calendar",
			"my email":     "your email",
			"the database": "the database",
			"rss":          "RSS feeds",
		} {
			if strings.Contains(low, phrase) && !seen[label] {
				seen[label] = true
				out = append(out, label)
			}
		}
	}
	sort.Strings(out)
	return out
}

// stageMarkers map an observable phrase onto the stage it implies. Ordered
// scanning keeps the stage list in the order the user described the work.
var stageMarkers = []struct {
	markers []string
	name    string
}{
	{[]string{"search", "find", "look up", "gather", "collect"}, "search sources"},
	{[]string{"fetch", "download", "read the article", "scrape"}, "fetch content"},
	{[]string{"filter", "curate", "select the", "pick the", "rank"}, "curate results"},
	{[]string{"summar", "brief", "digest"}, "summarize"},
	{[]string{"translate"}, "translate"},
	{[]string{"podcast", "audio", "voice", "narrat"}, "generate audio"},
	{[]string{"chart", "graph", "visuali"}, "build a chart"},
	{[]string{"analy", "compare", "evaluate"}, "analyze"},
	{[]string{"write", "draft", "compose", "report"}, "write the output"},
}

// sourcesListItems extracts the comma-separated items of an explicit
// "sources:" / "from:" clause, stopping at sentence end.
func sourcesListItems(original string) []string {
	low := strings.ToLower(original)
	var start int
	for _, marker := range []string{"sources:", "source:", "from these sources:"} {
		if i := strings.Index(low, marker); i >= 0 {
			start = i + len(marker)
			break
		}
	}
	if start == 0 {
		return nil
	}
	rest := original[start:]
	if i := strings.IndexAny(rest, ".\n"); i >= 0 {
		// A domain's dot must not end the clause — only a dot followed by a
		// space or end-of-text is a sentence boundary.
		for i >= 0 && i+1 < len(rest) && rest[i] == '.' && rest[i+1] != ' ' {
			next := strings.IndexAny(rest[i+1:], ".\n")
			if next < 0 {
				i = -1
				break
			}
			i = i + 1 + next
		}
		if i >= 0 {
			rest = rest[:i]
		}
	}
	var out []string
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(strings.Trim(part, " \t\"'"))
		if part != "" && len(part) < 60 {
			out = append(out, part)
		}
	}
	return out
}

func extractStages(low string) []SpecStage {
	var out []SpecStage
	seen := map[string]bool{}
	for _, sm := range stageMarkers {
		for _, marker := range sm.markers {
			idx := strings.Index(low, marker)
			if idx < 0 || seen[sm.name] {
				continue
			}
			seen[sm.name] = true
			out = append(out, SpecStage{Name: sm.name, Detail: excerptAround(low, idx)})
			break
		}
	}
	// Naming sources implies gathering them, even when no verb says so:
	// "Sources: A, B, C. Summarize the top stories" describes a fetch the user
	// considered too obvious to state.
	if !seen["search sources"] && (strings.Contains(low, "source") || len(reDomain.FindAllString(low, -1)) > 0) {
		out = append([]SpecStage{{Name: "search sources", Detail: "implied by the sources you named"}}, out...)
		seen["search sources"] = true
	}
	// Multiple named sources mean the first gathering stage fans out — the
	// user described parallel work even if they didn't use the word.
	if len(out) > 0 && (len(reDomain.FindAllString(low, -1)) > 1 || len(sourcesListItems(low)) > 1) {
		for i := range out {
			if out[i].Name == "search sources" || out[i].Name == "fetch content" {
				out[i].Parallel = true
				break
			}
		}
	}
	return out
}

// excerptAround returns a short window of the intent around a match, so the
// spec can show its evidence instead of asserting a stage exists.
func excerptAround(s string, idx int) string {
	start := idx - 40
	if start < 0 {
		start = 0
	}
	end := idx + 60
	if end > len(s) {
		end = len(s)
	}
	return strings.TrimSpace(reWhitespace.ReplaceAllString(s[start:end], " "))
}

func extractOutputs(low string) []string {
	var out []string
	for phrase, label := range map[string]string{
		"podcast": "an audio podcast", "audio": "an audio file",
		"summary": "a written summary", "brief": "a written briefing",
		"report": "a report", "chart": "a chart", "digest": "a digest",
	} {
		if strings.Contains(low, phrase) {
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return dedupeNonEmpty(out)
}

func extractDelivery(low string) []string {
	var out []string
	for phrase, label := range map[string]string{
		"telegram": "Telegram", "slack": "Slack", "discord": "Discord",
		"email": "email", "webhook": "an HTTP webhook", "whatsapp": "WhatsApp",
	} {
		if strings.Contains(low, phrase) {
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

func extractIntegrations(low string) []string {
	var out []string
	for phrase, label := range map[string]string{
		"notebooklm": "NotebookLM", "google drive": "Google Drive",
		"github": "GitHub", "notion": "Notion", "jira": "Jira",
		"spotify": "Spotify", "youtube": "YouTube",
	} {
		if strings.Contains(low, phrase) {
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

// deriveSecurity states the access this agent will need in plain language.
// Phrased as what it can DO, not which capability token it holds — consent is
// only informed if the user understands the consequence.
func deriveSecurity(s BuildSpec) []string {
	var out []string
	if len(s.Inputs) > 0 || containsAnyStage(s, "search sources", "fetch content") {
		out = append(out, "reads pages from the public web")
	}
	if len(s.Delivery) > 0 {
		out = append(out, "sends messages on your behalf to "+strings.Join(s.Delivery, " and "))
	}
	if len(s.Integrations) > 0 {
		out = append(out, "signs in to "+strings.Join(s.Integrations, ", ")+" using stored credentials")
	}
	return out
}

func containsAnyStage(s BuildSpec, names ...string) bool {
	for _, st := range s.Stages {
		for _, n := range names {
			if st.Name == n {
				return true
			}
		}
	}
	return false
}

// deriveQuestions asks for what is missing. A question is a BLOCKER only when
// no sensible default exists — over-blocking turns a guided flow into an
// interrogation, and every question here has to earn its place.
func deriveQuestions(s BuildSpec, low string) []SpecQuestion {
	var qs []SpecQuestion

	if len(s.Stages) == 0 {
		qs = append(qs, SpecQuestion{
			ID: "stages", Question: "What should this agent actually do with the information?",
			Why:     "Studio could not identify any processing step, so there is nothing to build yet.",
			Blocker: true, Field: "intent",
		})
	}
	if s.Trigger == "schedule" && s.Schedule == "" {
		qs = append(qs, SpecQuestion{
			ID: "schedule_time", Question: "What time of day should this run?",
			Why:     "A scheduled agent needs an exact time — otherwise it cannot be put on a calendar.",
			Blocker: true, Field: "schedule",
		})
	}
	if len(s.Delivery) > 0 {
		// Naming a channel is not naming a destination, and this is the single
		// most common cause of a run that "succeeds" and reaches nobody.
		if !hasExplicitDestination(low) {
			qs = append(qs, SpecQuestion{
				ID: "destination", Question: "Where exactly should the result be delivered (which chat, channel, or address)?",
				Why:     "Studio knows the channel but not the destination, and a message with no destination is delivered to nobody.",
				Blocker: true, Field: "delivery",
			})
		}
	}
	if len(s.Delivery) == 0 && len(s.Outputs) > 0 {
		qs = append(qs, SpecQuestion{
			ID: "delivery", Question: "Where should the result go when it's ready?",
			Why:     "This produces something, but the intent doesn't say who receives it.",
			Blocker: false, Field: "delivery",
		})
	}
	if len(s.Integrations) > 0 {
		qs = append(qs, SpecQuestion{
			ID: "integration_auth", Question: "Confirm the sign-in for " + strings.Join(s.Integrations, ", ") + ".",
			Why:     "These services need stored credentials before the workflow can use them.",
			Blocker: false, Field: "integrations",
		})
	}
	if len(s.Inputs) == 0 && containsAnyStage(s, "search sources", "fetch content") {
		qs = append(qs, SpecQuestion{
			ID: "sources", Question: "Which sources should it read?",
			Why:     "Narrowing the sources makes results far more predictable than an open web search.",
			Blocker: false, Field: "inputs",
		})
	}
	return qs
}

// hasExplicitDestination reports whether the intent names WHERE to deliver, as
// opposed to merely which channel. A naive "does it contain a digit" test is
// satisfied by the schedule ("8:00am"), which is how "delivered to nobody"
// became the most common silent failure.
func hasExplicitDestination(low string) bool {
	return reDestination.MatchString(low)
}

// ── Refinement ──────────────────────────────────────────────────────────────

// SpecChange is one difference between two specs, for the visible change
// summary ST-01 requires. A refinement the user cannot inspect is a refinement
// they cannot trust.
type SpecChange struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Kind   string `json:"kind"` // added | removed | changed
}

// DiffSpecs reports what refinement changed. Used to prove a refine produced a
// materially different spec rather than a reworded one.
func DiffSpecs(before, after BuildSpec) []SpecChange {
	var out []SpecChange
	cmp := func(field, b, a string) {
		if b == a {
			return
		}
		switch {
		case b == "":
			out = append(out, SpecChange{Field: field, After: a, Kind: "added"})
		case a == "":
			out = append(out, SpecChange{Field: field, Before: b, Kind: "removed"})
		default:
			out = append(out, SpecChange{Field: field, Before: b, After: a, Kind: "changed"})
		}
	}
	cmp("trigger", before.Trigger, after.Trigger)
	cmp("schedule", before.Schedule, after.Schedule)
	cmpList := func(field string, b, a []string) {
		cmp(field, strings.Join(b, ", "), strings.Join(a, ", "))
	}
	cmpList("inputs", before.Inputs, after.Inputs)
	cmpList("outputs", before.Outputs, after.Outputs)
	cmpList("delivery", before.Delivery, after.Delivery)
	cmpList("integrations", before.Integrations, after.Integrations)
	cmpList("stages", stageNames(before), stageNames(after))
	return out
}

func stageNames(s BuildSpec) []string {
	out := make([]string, 0, len(s.Stages))
	for _, st := range s.Stages {
		out = append(out, st.Name)
	}
	return out
}

// MateriallyDifferent reports whether a refinement actually changed the BUILD,
// as opposed to only rewording the prose. ST-01 requires refine to produce a
// build-ready spec, and a refine that changes nothing structural should say so
// rather than implying progress.
func MateriallyDifferent(before, after BuildSpec) bool {
	for _, c := range DiffSpecs(before, after) {
		if c.Field != "intent" {
			return true
		}
	}
	// Resolving a blocker is material even when no field changed shape.
	return len(before.Blockers()) != len(after.Blockers())
}
