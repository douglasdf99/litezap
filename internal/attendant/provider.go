package attendant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Blob is a file passed to a model (image or document).
type Blob struct {
	Mime string
	Name string
	Data []byte
}

// Part is one piece of a message: text, an image or a document.
type Part struct {
	Text     string
	Image    *Blob
	Document *Blob
}

// ChatMessage is a turn sent to the model.
type ChatMessage struct {
	Role  string // user | assistant
	Parts []Part
}

// ChatRequest asks the model for one answer.
type ChatRequest struct {
	System    string
	Messages  []ChatMessage
	MaxTokens int
	// Schema, when set, asks for a JSON object with this JSON schema.
	Schema json.RawMessage
	// User is a pseudonymous end-user id (abuse monitoring), never a phone number.
	User string
}

// ChatResponse is the model's answer.
type ChatResponse struct {
	Text         string
	Model        string
	InputTokens  int64
	OutputTokens int64
	// Refused: the provider declined to answer (safety); the agent hands off.
	Refused bool
}

// Provider is an LLM chat API.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// Supports reports whether a blob of this mime type can be sent as image/document.
	SupportsImage(mime string) bool
	SupportsDocument(mime string) bool
}

// Transcriber turns a voice note into text.
type Transcriber interface {
	Transcribe(ctx context.Context, audio Blob) (string, error)
}

// Speaker synthesizes a voice note (Ogg/Opus).
type Speaker interface {
	Speak(ctx context.Context, text string) (Blob, error)
}

// Backends are the provider clients built from an instance's settings; nil members are
// not configured.
type Backends struct {
	Chat        Provider
	Transcriber Transcriber
	Speaker     Speaker
}

// Factory builds the backends of a configuration (tests inject fakes).
type Factory func(s Settings, sec Secrets) (Backends, error)

// providerTimeout bounds one provider HTTP call (the hook timeout bounds the whole answer).
const providerTimeout = 90 * time.Second

// httpClient is the client of OpenAI-compatible calls: no environment proxy, no redirects
// (the key must not follow one), bounded.
func httpClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &http.Client{Transport: tr, Timeout: providerTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// DefaultFactory builds real clients.
func DefaultFactory(s Settings, sec Secrets) (Backends, error) {
	var b Backends
	switch s.Provider {
	case ProviderAnthropic:
		if sec.APIKey == "" {
			return b, errors.New("no Anthropic API key")
		}
		b.Chat = newAnthropic(s, sec.APIKey)
	case ProviderOpenAI:
		b.Chat = &openAIChat{base: s.BaseURL, key: sec.APIKey, model: s.Model, client: httpClient(), noUser: !s.sendsUser()}
	default:
		return b, errors.New("unknown provider")
	}
	if base := s.audioBaseURL(); base != "" {
		key := s.audioKey(sec)
		if s.Audio.TranscriptionModel != "" {
			b.Transcriber = &openAIAudio{base: base, key: key, model: s.Audio.TranscriptionModel, client: httpClient()}
		}
		if s.Audio.SpeechModel != "" {
			b.Speaker = &openAIAudio{base: base, key: key, model: s.Audio.SpeechModel, voice: s.Audio.Voice, client: httpClient()}
		}
	}
	return b, nil
}
