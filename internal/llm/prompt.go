package llm

import (
	_ "embed"
	"encoding/json"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

//go:embed system_prompt.txt
var systemPrompt string

// analysisSchema is the JSON schema sent to providers that support
// constrained output. It is written in OpenAI strict-mode form (every
// property required, no additional properties), which Anthropic also accepts.
var analysisSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": [
    "summary",
    "root_cause",
    "severity",
    "affected_resources",
    "evidence",
    "possible_causes",
    "recommended_actions",
    "safe_commands",
    "schema_version",
    "verdict",
    "support",
    "missing_data",
    "limitations"
  ],
  "properties": {
    "summary": {
      "type": "string"
    },
    "root_cause": {
      "type": "string"
    },
    "severity": {
      "type": "string",
      "enum": [
        "low",
        "medium",
        "high",
        "critical"
      ]
    },
    "affected_resources": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": [
          "source",
          "description",
          "evidence_id"
        ],
        "properties": {
          "source": {
            "type": "string"
          },
          "description": {
            "type": "string"
          },
          "evidence_id": {
            "type": "string"
          }
        }
      }
    },
    "possible_causes": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "recommended_actions": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "safe_commands": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "schema_version": {
      "type": "integer",
      "enum": [
        2
      ]
    },
    "verdict": {
      "type": "string",
      "enum": [
        "observed_fault",
        "suspected_fault",
        "no_fault_observed",
        "inconclusive"
      ]
    },
    "support": {
      "type": "string",
      "enum": [
        "strong",
        "limited",
        "insufficient"
      ]
    },
    "missing_data": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "limitations": {
      "type": "array",
      "items": {
        "type": "string"
      }
    }
  }
}`)

type promptIncident struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	domain.Target
}

type promptFailure struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

type promptEvidence struct {
	ID      string          `json:"id"`
	Source  string          `json:"source"`
	Subject string          `json:"subject"`
	Health  domain.Health   `json:"health"`
	Summary string          `json:"summary"`
	Signals []string        `json:"signals"`
	Data    json.RawMessage `json:"data"`
}

// userPrompt renders the incident and evidence as JSON. Only fields useful
// for analysis are included; evidence IDs permit exact citations; incident IDs and timestamps are omitted.
func userPrompt(in AnalysisInput) (string, error) {
	in.Incident = in.Incident.Redacted()
	msg := struct {
		Incident          promptIncident   `json:"incident"`
		Evidence          []promptEvidence `json:"evidence"`
		FailedDiagnostics []promptFailure  `json:"failed_diagnostics,omitempty"`
		Limitations       []string         `json:"limitations"`
	}{
		Incident: promptIncident{Title: in.Incident.Title, Description: in.Incident.Description, Target: in.Incident.Target},
	}
	for _, limit := range in.Limitations {
		msg.Limitations = append(msg.Limitations, redact.Text(limit))
	}
	for _, e := range in.Evidence {
		data, err := redact.JSON(e.Data)
		if err != nil {
			return "", err
		}
		msg.Evidence = append(msg.Evidence, promptEvidence{
			ID: e.ID, Source: e.Source, Subject: redact.Text(e.Subject), Health: e.Health, Summary: redact.Text(e.Summary), Signals: e.Signals, Data: data,
		})
	}
	for _, te := range in.Failed {
		msg.FailedDiagnostics = append(msg.FailedDiagnostics, promptFailure{Source: te.ToolName, Error: redact.Text(te.Error)})
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
