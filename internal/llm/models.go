package llm

import (
	"context"
	"net/url"
	"strings"
)

// Each provider lists the models its key may use, for CheckModel at startup.

func (p *OpenAIProvider) Models(ctx context.Context) ([]string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, p.client, p.baseURL+"/models", map[string]string{"Authorization": "Bearer " + p.apiKey}, &resp); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func (p *AnthropicProvider) Models(ctx context.Context) ([]string, error) {
	headers := map[string]string{"x-api-key": p.apiKey, "anthropic-version": anthropicVersion}
	var ids []string
	for after := ""; ; {
		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		u := p.baseURL + "/v1/models?limit=1000"
		if after != "" {
			u += "&after_id=" + url.QueryEscape(after)
		}
		if err := getJSON(ctx, p.client, u, headers, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data {
			ids = append(ids, m.ID)
		}
		if !resp.HasMore || resp.LastID == "" || resp.LastID == after {
			return ids, nil
		}
		after = resp.LastID
	}
}

// Models lists only models that support generateContent, without the
// "models/" prefix, as LLM_MODEL names them.
func (p *GeminiProvider) Models(ctx context.Context) ([]string, error) {
	var ids []string
	for token := ""; ; {
		var resp struct {
			Models []struct {
				Name    string   `json:"name"`
				Methods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			Next string `json:"nextPageToken"`
		}
		u := p.baseURL + "/models?pageSize=1000"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		if err := getJSON(ctx, p.client, u, map[string]string{"x-goog-api-key": p.apiKey}, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Models {
			for _, method := range m.Methods {
				if method == "generateContent" {
					ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
					break
				}
			}
		}
		if resp.Next == "" || resp.Next == token {
			return ids, nil
		}
		token = resp.Next
	}
}
