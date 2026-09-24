package llm

import (
	_ "embed"
	"encoding/json"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

//go:embed system_prompt.txt
var systemPrompt string

// analysisSchema is the JSON schema sent to providers that support
// constrained output. It is written in OpenAI strict-mode form (every
// property required, no additional properties), which Ollama also accepts.
var analysisSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "root_cause", "confidence", "severity", "affected_resources", "evidence", "possible_causes", "recommended_actions", "safe_commands"],
  "properties": {
    "summary": {"type": "string"},
    "root_cause": {"type": "string"},
    "confidence": {"type": "number"},
    "severity": {"type": "string", "enum": ["low", "medium", "high", "critical"]},
    "affected_resources": {"type": "array", "items": {"type": "string"}},
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["source", "description"],
        "properties": {"source": {"type": "string"}, "description": {"type": "string"}}
      }
    },
    "possible_causes": {"type": "array", "items": {"type": "string"}},
    "recommended_actions": {"type": "array", "items": {"type": "string"}},
    "safe_commands": {"type": "array", "items": {"type": "string"}}
  }
}`)

type promptIncident struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	domain.Target
}

type promptEvidence struct {
	Source  string          `json:"source"`
	Subject string          `json:"subject"`
	Health  domain.Health   `json:"health"`
	Summary string          `json:"summary"`
	Signals []string        `json:"signals"`
	Data    json.RawMessage `json:"data"`
}

// userPrompt renders the incident and evidence as JSON. Only fields useful
// for analysis are included; database IDs and timestamps are omitted.
func userPrompt(in AnalysisInput) (string, error) {
	msg := struct {
		Incident promptIncident   `json:"incident"`
		Evidence []promptEvidence `json:"evidence"`
	}{
		Incident: promptIncident{Title: in.Incident.Title, Description: in.Incident.Description, Target: in.Incident.Target},
	}
	for _, e := range in.Evidence {
		msg.Evidence = append(msg.Evidence, promptEvidence{
			Source: e.Source, Subject: e.Subject, Health: e.Health, Summary: e.Summary, Signals: e.Signals, Data: e.Data,
		})
	}
	b, err := json.Marshal(msg)
	return string(b), err
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func messages(in AnalysisInput) ([]chatMessage, error) {
	user, err := userPrompt(in)
	if err != nil {
		return nil, err
	}
	return []chatMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: user}}, nil
}
