package attendant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// openAIChat calls POST {base}/chat/completions of an OpenAI-compatible server.
type openAIChat struct {
	base, key, model string
	client           *http.Client
	noUser           bool // the server rejects the "user" field (Gemini)
}

func (c *openAIChat) SupportsImage(mime string) bool {
	switch baseMime(mime) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

func (c *openAIChat) SupportsDocument(string) bool { return false }

const maxProviderBody = 4 << 20

func (c *openAIChat) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	type content struct {
		Type     string            `json:"type"`
		Text     string            `json:"text,omitempty"`
		ImageURL map[string]string `json:"image_url,omitempty"`
	}
	type message struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	msgs := []message{{Role: "system", Content: req.System}}
	for _, m := range req.Messages {
		var parts []content
		var text []string
		hasImage := false
		for _, p := range m.Parts {
			switch {
			case p.Image != nil:
				hasImage = true
				parts = append(parts, content{Type: "image_url", ImageURL: map[string]string{
					"url": "data:" + baseMime(p.Image.Mime) + ";base64," + base64.StdEncoding.EncodeToString(p.Image.Data)}})
			case p.Text != "":
				parts = append(parts, content{Type: "text", Text: p.Text})
				text = append(text, p.Text)
			}
		}
		if hasImage {
			msgs = append(msgs, message{Role: m.Role, Content: parts})
		} else if len(text) > 0 { // plain strings work with every compatible server
			msgs = append(msgs, message{Role: m.Role, Content: strings.Join(text, "\n\n")})
		}
	}
	body := map[string]any{"model": c.model, "messages": msgs, "max_tokens": req.MaxTokens}
	if len(req.Schema) > 0 {
		// json_object is the widest supported structured mode; the schema is in the prompt.
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if req.User != "" && !c.noUser {
		body["user"] = req.User
	}
	raw, status, err := c.post(ctx, "/chat/completions", body)
	if err != nil {
		return ChatResponse{}, err
	}
	if status == http.StatusBadRequest && len(req.Schema) > 0 {
		// Some local servers reject response_format: retry once without it.
		delete(body, "response_format")
		raw, status, err = c.post(ctx, "/chat/completions", body)
		if err != nil {
			return ChatResponse{}, err
		}
	}
	if status < 200 || status > 299 {
		return ChatResponse{}, fmt.Errorf("chat API: HTTP %d", status)
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return ChatResponse{}, errors.New("chat API: unexpected response")
	}
	ch := out.Choices[0]
	r := ChatResponse{Text: strings.TrimSpace(ch.Message.Content), Model: out.Model,
		InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	if ch.Message.Refusal != "" || ch.FinishReason == "content_filter" {
		r.Refused = true
	}
	return r, nil
}

func (c *openAIChat) post(ctx context.Context, path string, body any) ([]byte, int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return nil, 0, errors.New("chat API: invalid base URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	return do(c.client, req, "chat API")
}

// do runs a request and reads a bounded body; errors never include the URL or the key.
func do(client *http.Client, req *http.Request, what string) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, 0, err
		}
		return nil, 0, fmt.Errorf("%s: request failed", what)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%s: read response", what)
	}
	if len(raw) > maxProviderBody {
		return nil, 0, fmt.Errorf("%s: response too large", what)
	}
	return raw, resp.StatusCode, nil
}

// openAIAudio calls /audio/transcriptions and /audio/speech of an OpenAI-compatible server.
type openAIAudio struct {
	base, key, model, voice string
	client                  *http.Client
}

func (a *openAIAudio) Transcribe(ctx context.Context, audio Blob) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	name := audio.Name
	if name == "" {
		name = "audio.ogg"
	}
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(audio.Data); err != nil {
		return "", err
	}
	_ = w.WriteField("model", a.model)
	_ = w.WriteField("response_format", "json")
	if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/audio/transcriptions", &buf)
	if err != nil {
		return "", errors.New("transcription API: invalid base URL")
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	raw, status, err := do(a.client, req, "transcription API")
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", fmt.Errorf("transcription API: HTTP %d", status)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("transcription API: unexpected response")
	}
	return strings.TrimSpace(out.Text), nil
}

// Speak asks for Ogg/Opus, the format of WhatsApp voice notes.
func (a *openAIAudio) Speak(ctx context.Context, text string) (Blob, error) {
	voice := a.voice
	if voice == "" {
		voice = "alloy"
	}
	b, _ := json.Marshal(map[string]string{"model": a.model, "input": text, "voice": voice, "response_format": "opus"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/audio/speech", bytes.NewReader(b))
	if err != nil {
		return Blob{}, errors.New("speech API: invalid base URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	raw, status, err := do(a.client, req, "speech API")
	if err != nil {
		return Blob{}, err
	}
	if status < 200 || status > 299 {
		return Blob{}, fmt.Errorf("speech API: HTTP %d", status)
	}
	if len(raw) < 32 {
		return Blob{}, errors.New("speech API: empty audio")
	}
	return Blob{Mime: "audio/ogg; codecs=opus", Name: "resposta.ogg", Data: raw}, nil
}
