package attendant

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicChat calls the Claude API through the official SDK.
type anthropicChat struct {
	client   anthropic.Client
	model    string
	official bool // the Claude API itself (not a custom base URL): fallbacks are available
}

func newAnthropic(s Settings, key string) *anthropicChat {
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(0), // one provider call per reserved slot
		option.WithRequestTimeout(providerTimeout), option.WithHTTPClient(httpClient())}
	if s.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(s.BaseURL))
	}
	return &anthropicChat{client: anthropic.NewClient(opts...), model: s.Model, official: s.BaseURL == ""}
}

var anthropicImageMimes = map[string]anthropic.BetaBase64ImageSourceMediaType{
	"image/jpeg": anthropic.BetaBase64ImageSourceMediaTypeImageJPEG,
	"image/png":  anthropic.BetaBase64ImageSourceMediaTypeImagePNG,
	"image/gif":  anthropic.BetaBase64ImageSourceMediaTypeImageGIF,
	"image/webp": anthropic.BetaBase64ImageSourceMediaTypeImageWebP,
}

func (c *anthropicChat) SupportsImage(mime string) bool {
	_, ok := anthropicImageMimes[baseMime(mime)]
	return ok
}

func (c *anthropicChat) SupportsDocument(mime string) bool {
	return baseMime(mime) == "application/pdf"
}

// fallbackModels accept the server-side refusal fallback ("default" mode).
func fallbackModel(m string) bool {
	return m == "claude-sonnet-5-5" || m == "claude-opus-5-5" || m == "claude-opus-5" || strings.HasPrefix(m, "claude-fable-5")
}

func (c *anthropicChat) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	msgs := make([]anthropic.BetaMessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := anthropic.BetaMessageParamRoleUser
		if m.Role == "assistant" {
			role = anthropic.BetaMessageParamRoleAssistant
		}
		var blocks []anthropic.BetaContentBlockParamUnion
		for _, p := range m.Parts {
			switch {
			case p.Image != nil:
				blocks = append(blocks, anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
					Data: base64.StdEncoding.EncodeToString(p.Image.Data), MediaType: anthropicImageMimes[baseMime(p.Image.Mime)]}))
			case p.Document != nil:
				blocks = append(blocks, anthropic.NewBetaDocumentBlock(anthropic.BetaBase64PDFSourceParam{
					Data: base64.StdEncoding.EncodeToString(p.Document.Data)}))
			case p.Text != "":
				blocks = append(blocks, anthropic.NewBetaTextBlock(p.Text))
			}
		}
		if len(blocks) > 0 {
			msgs = append(msgs, anthropic.BetaMessageParam{Role: role, Content: blocks})
		}
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: int64(req.MaxTokens),
		System:    []anthropic.BetaTextBlockParam{{Text: req.System}},
		Messages:  msgs,
		// Chat replies: low effort keeps answers fast and cheap.
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort("low")},
	}
	if len(req.Schema) > 0 {
		params.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: req.Schema}
	}
	if req.User != "" {
		params.Metadata = anthropic.BetaMetadataParam{UserID: anthropic.String(req.User)}
	}
	if c.official && fallbackModel(c.model) {
		// A refused request is re-served by a fallback model chosen by the API.
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	resp, err := c.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return ChatResponse{}, cleanProviderErr(err)
	}
	out := ChatResponse{Model: string(resp.Model), InputTokens: resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens +
		resp.Usage.CacheReadInputTokens, OutputTokens: resp.Usage.OutputTokens}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		out.Refused = true
		return out, nil
	}
	var texts []string
	for _, block := range resp.Content {
		if b, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			texts = append(texts, b.Text)
		}
	}
	out.Text = strings.TrimSpace(strings.Join(texts, "\n"))
	return out, nil
}

// cleanProviderErr keeps the status and the API's message, never request details.
func cleanProviderErr(err error) error {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		return fmt.Errorf("anthropic API: HTTP %d", ae.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return errors.New("anthropic API: request failed")
}

func baseMime(m string) string {
	m, _, _ = strings.Cut(m, ";")
	return strings.ToLower(strings.TrimSpace(m))
}
