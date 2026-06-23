package llm

import (
	"context"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// ListModels returns the model ids advertised by the OpenAI-compatible gateway
// at baseURL (its GET /models endpoint). It is used to populate the per-speaker
// model pickers with whatever the gateway actually serves, rather than a
// hard-coded roster. The ids are returned in the order the gateway lists them.
func ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	c := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
	)
	pager := c.Models.ListAutoPaging(ctx)
	var ids []string
	for pager.Next() {
		if id := pager.Current().ID; id != "" {
			ids = append(ids, id)
		}
	}
	if err := pager.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}
