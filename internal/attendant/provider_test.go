package attendant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleChat(t *testing.T) {
	var got map[string]any
	var auth string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"model":"llama3","choices":[{"message":{"content":"{\"reply\":\"oi\"}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":3}}`)
	}))
	defer srv.Close()
	b, err := DefaultFactory(Settings{Provider: ProviderOpenAI, BaseURL: srv.URL + "/v1", Model: "llama3"}, Secrets{APIKey: "k-123"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.Chat.Chat(context.Background(), ChatRequest{System: "sys", MaxTokens: 100, Schema: outputSchema, User: "lz_x",
		Messages: []ChatMessage{{Role: "user", Parts: []Part{{Text: "olá"}, {Image: &Blob{Mime: "image/png", Data: []byte("png")}}}}}})
	if err != nil || resp.Text != `{"reply":"oi"}` || resp.InputTokens != 12 || resp.OutputTokens != 3 {
		t.Fatalf("resp %+v %v", resp, err)
	}
	if auth != "Bearer k-123" || got["model"] != "llama3" || got["user"] != "lz_x" {
		t.Fatalf("request %v auth %q", got, auth)
	}
	msgs := got["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages %v", msgs)
	}
	parts := msgs[1].(map[string]any)["content"].([]any)
	if url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image %v", parts)
	}
	if got["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("response_format %v", got["response_format"])
	}
	if b.Transcriber != nil || b.Speaker != nil {
		t.Fatal("audio configured without models")
	}
}

func TestOpenAIRetriesWithoutResponseFormatAndHidesErrors(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["response_format"]; ok {
			http.Error(w, "unsupported response_format", 400)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"texto"}}]}`)
	}))
	defer srv.Close()
	c := &openAIChat{base: srv.URL, key: "secret-key-xyz", model: "m", client: httpClient()}
	resp, err := c.Chat(context.Background(), ChatRequest{Schema: outputSchema, Messages: []ChatMessage{{Role: "user", Parts: []Part{{Text: "a"}}}}})
	if err != nil || resp.Text != "texto" || n != 2 {
		t.Fatalf("resp %+v %v n=%d", resp, err, n)
	}
	// A redirect is never followed (the key would go along) and errors carry no key or URL.
	red := httptest.NewServer(http.RedirectHandler(srv.URL, http.StatusTemporaryRedirect))
	defer red.Close()
	c.base = red.URL
	_, err = c.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Parts: []Part{{Text: "a"}}}}})
	if err == nil || strings.Contains(err.Error(), "secret-key-xyz") || strings.Contains(err.Error(), red.URL) {
		t.Fatalf("redirect: %v", err)
	}
	c.base = "http://127.0.0.1:1"
	_, err = c.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Parts: []Part{{Text: "a"}}}}})
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("network error leaks the URL: %v", err)
	}
}

func TestOpenAIAudio(t *testing.T) {
	var model, voice, format string
	var file []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/transcriptions":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			model = r.FormValue("model")
			f, _, _ := r.FormFile("file")
			file, _ = io.ReadAll(f)
			_, _ = io.WriteString(w, `{"text":" olá mundo "}`)
		case "/v1/audio/speech":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			voice, format = body["voice"], body["response_format"]
			_, _ = w.Write([]byte("OggS" + strings.Repeat("x", 60)))
		}
	}))
	defer srv.Close()
	b, err := DefaultFactory(Settings{Provider: ProviderAnthropic, Model: DefaultModel,
		Audio: AudioSettings{BaseURL: srv.URL + "/v1", TranscriptionModel: "whisper-1", SpeechModel: "tts-1", Voice: "nova"}},
		Secrets{APIKey: "a", AudioAPIKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	txt, err := b.Transcriber.Transcribe(context.Background(), Blob{Mime: "audio/ogg", Data: []byte("AUDIO")})
	if err != nil || txt != "olá mundo" || model != "whisper-1" || string(file) != "AUDIO" {
		t.Fatalf("transcribe %q %v model=%q file=%q", txt, err, model, file)
	}
	blob, err := b.Speaker.Speak(context.Background(), "oi")
	if err != nil || !strings.HasPrefix(string(blob.Data), "OggS") || voice != "nova" || format != "opus" {
		t.Fatalf("speak %v voice=%q format=%q", err, voice, format)
	}
}

func TestAnthropicProviderRequest(t *testing.T) {
	var got map[string]any
	var key, beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, beta = r.Header.Get("X-Api-Key"), r.Header.Get("Anthropic-Beta")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5",
			"content":[{"type":"text","text":"{\"reply\":\"olá\"}"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":50,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":10}}`)
	}))
	defer srv.Close()
	c := newAnthropic(Settings{Provider: ProviderAnthropic, Model: DefaultModel, BaseURL: srv.URL}, "sk-test")
	resp, err := c.Chat(context.Background(), ChatRequest{System: "sys", MaxTokens: 256, Schema: outputSchema, User: "lz_1",
		Messages: []ChatMessage{{Role: "user", Parts: []Part{{Text: "oi"}, {Image: &Blob{Mime: "image/jpeg", Data: []byte("j")}},
			{Document: &Blob{Mime: "application/pdf", Data: []byte("%PDF")}}}}}})
	if err != nil || resp.Text != `{"reply":"olá"}` || resp.InputTokens != 60 || resp.OutputTokens != 7 {
		t.Fatalf("resp %+v %v", resp, err)
	}
	if key != "sk-test" || got["model"] != DefaultModel || got["max_tokens"].(float64) != 256 {
		t.Fatalf("request %v key %q", got, key)
	}
	oc := got["output_config"].(map[string]any)
	if oc["effort"] != "low" || oc["format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("output_config %v", oc)
	}
	content := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 3 || content[1].(map[string]any)["type"] != "image" || content[2].(map[string]any)["type"] != "document" {
		t.Fatalf("content %v", content)
	}
	if got["metadata"].(map[string]any)["user_id"] != "lz_1" {
		t.Fatalf("metadata %v", got["metadata"])
	}
	// A custom base URL is not the Claude API: no fallback beta.
	if strings.Contains(beta, "fallback") || got["fallbacks"] != nil {
		t.Fatalf("fallbacks sent to a custom base URL: %q %v", beta, got["fallbacks"])
	}
	// Errors keep the status only.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key sk-test"}}`)
	})
	_, err = c.Chat(context.Background(), ChatRequest{System: "s", MaxTokens: 64, Messages: []ChatMessage{{Role: "user", Parts: []Part{{Text: "oi"}}}}})
	if err == nil || strings.Contains(err.Error(), "sk-test") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error %v", err)
	}
}
