package attendant

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
)

// Providers.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai" // any OpenAI-compatible server (OpenAI, Ollama, LM Studio, vLLM...)
)

// DefaultModel is the default Anthropic model.
const DefaultModel = "claude-sonnet-5-5"

// Settings is an instance's attendant configuration, set by the admin (no secrets).
type Settings struct {
	Provider string `json:"provider"`
	// Preset is the free provider preset this configuration came from (ollama,
	// gemini_free, groq_free; "" = none). It only names defaults over the openai
	// provider: the UI shows it and usage reports free calls.
	Preset string `json:"preset"`
	Model  string `json:"model"`
	// BaseURL of an OpenAI-compatible server (e.g. https://api.openai.com/v1 or
	// http://127.0.0.1:11434/v1); ignored for anthropic.
	BaseURL string `json:"base_url"`
	// MaxTokens bounds one answer (default 1024).
	MaxTokens int `json:"max_tokens"`
	// DailyCallCap bounds provider calls per UTC day (chat, transcription, speech;
	// default 300; 0 = no limit, discouraged).
	DailyCallCap int `json:"daily_call_cap"`
	// ChatDailyCallCap bounds provider calls per chat per UTC day (default 40; 0 = no
	// per-chat limit): one chat cannot drain the instance's cap.
	ChatDailyCallCap int `json:"chat_daily_call_cap"`
	// SimulateDailyCap bounds simulator calls per UTC day (default 50; they also count in
	// daily_call_cap).
	SimulateDailyCap int `json:"simulate_daily_cap"`
	// ActiveAgentID is the agent that answers (default: the first one created).
	ActiveAgentID string `json:"active_agent_id"`
	// Vision passes images (and PDFs, on anthropic) to the model.
	Vision bool          `json:"vision"`
	Audio  AudioSettings `json:"audio"`
	// Prices in USD per million tokens, for the cost log (0: built-in table or unknown).
	PriceInputPerMTok  float64 `json:"price_input_per_mtok"`
	PriceOutputPerMTok float64 `json:"price_output_per_mtok"`
}

// AudioSettings configures an OpenAI-compatible audio server: /audio/transcriptions for
// incoming voice notes and /audio/speech for voice replies.
type AudioSettings struct {
	// BaseURL; empty with the openai provider means the same server.
	BaseURL string `json:"base_url"`
	// TranscriptionModel (e.g. whisper-1); empty turns transcription off.
	TranscriptionModel string `json:"transcription_model"`
	// SpeechModel (e.g. gpt-4o-mini-tts); empty turns voice replies off.
	SpeechModel string `json:"speech_model"`
	Voice       string `json:"voice"`
}

// Secrets are the provider keys, stored encrypted and never returned.
type Secrets struct {
	APIKey      string `json:"api_key,omitempty"`
	AudioAPIKey string `json:"audio_api_key,omitempty"`
}

// DefaultSettings is the starting point of a new configuration.
func DefaultSettings() Settings {
	return Settings{Provider: ProviderAnthropic, Model: DefaultModel, MaxTokens: 1024, DailyCallCap: 300,
		ChatDailyCallCap: 40, SimulateDailyCap: 50, Vision: true}
}

// SettingsPatch changes some fields (nil = keep). A key set to "" removes it.
type SettingsPatch struct {
	Provider           *string        `json:"provider"`
	Preset             *string        `json:"preset"`
	Model              *string        `json:"model"`
	BaseURL            *string        `json:"base_url"`
	MaxTokens          *int           `json:"max_tokens"`
	DailyCallCap       *int           `json:"daily_call_cap"`
	ChatDailyCallCap   *int           `json:"chat_daily_call_cap"`
	SimulateDailyCap   *int           `json:"simulate_daily_cap"`
	Vision             *bool          `json:"vision"`
	Audio              *AudioSettings `json:"audio"`
	PriceInputPerMTok  *float64       `json:"price_input_per_mtok"`
	PriceOutputPerMTok *float64       `json:"price_output_per_mtok"`
	APIKey             *string        `json:"api_key"`
	AudioAPIKey        *string        `json:"audio_api_key"`
}

func (p SettingsPatch) apply(s Settings, sec Secrets) (Settings, Secrets) {
	str := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	str(&s.Provider, p.Provider)
	str(&s.Preset, p.Preset)
	str(&s.Model, p.Model)
	str(&s.BaseURL, p.BaseURL)
	if p.MaxTokens != nil {
		s.MaxTokens = *p.MaxTokens
	}
	if p.DailyCallCap != nil {
		s.DailyCallCap = *p.DailyCallCap
	}
	if p.ChatDailyCallCap != nil {
		s.ChatDailyCallCap = *p.ChatDailyCallCap
	}
	if p.SimulateDailyCap != nil {
		s.SimulateDailyCap = *p.SimulateDailyCap
	}
	if p.Vision != nil {
		s.Vision = *p.Vision
	}
	if p.Audio != nil {
		a := *p.Audio
		a.BaseURL, a.TranscriptionModel = strings.TrimSpace(a.BaseURL), strings.TrimSpace(a.TranscriptionModel)
		a.SpeechModel, a.Voice = strings.TrimSpace(a.SpeechModel), strings.TrimSpace(a.Voice)
		s.Audio = a
	}
	if p.PriceInputPerMTok != nil {
		s.PriceInputPerMTok = *p.PriceInputPerMTok
	}
	if p.PriceOutputPerMTok != nil {
		s.PriceOutputPerMTok = *p.PriceOutputPerMTok
	}
	str(&sec.APIKey, p.APIKey)
	str(&sec.AudioAPIKey, p.AudioAPIKey)
	return s, sec
}

func validBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		u.RawQuery == "" && u.Fragment == "" && len(raw) <= 2048
}

// Validate checks the settings with their secrets.
func (s *Settings) Validate(sec Secrets) error {
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	s.Audio.BaseURL = strings.TrimRight(s.Audio.BaseURL, "/")
	if s.Model == "" && s.Provider == ProviderAnthropic {
		s.Model = DefaultModel
	}
	if _, ok := presetByID(s.Preset); s.Preset != "" && !ok {
		return errors.New(`preset must be empty, "ollama", "gemini_free" or "groq_free"`)
	}
	switch {
	case s.Preset != "" && s.Provider != ProviderOpenAI:
		return errors.New("a preset uses the openai provider")
	case s.Provider != ProviderAnthropic && s.Provider != ProviderOpenAI:
		return errors.New(`provider must be "anthropic" or "openai" (any OpenAI-compatible server)`)
	case s.Model == "" || len(s.Model) > 200:
		return errors.New("model is required (at most 200 characters)")
	case s.Provider == ProviderOpenAI && !validBaseURL(s.BaseURL):
		return errors.New("base_url must be the http(s) URL of the OpenAI-compatible API (e.g. https://api.openai.com/v1), without credentials or query")
	case s.Provider == ProviderAnthropic && s.BaseURL != "" && !validBaseURL(s.BaseURL):
		return errors.New("base_url must be an http(s) URL")
	case s.Audio.BaseURL != "" && !validBaseURL(s.Audio.BaseURL):
		return errors.New("audio.base_url must be an http(s) URL without credentials or query")
	case s.MaxTokens < 64 || s.MaxTokens > 8192:
		return errors.New("max_tokens must be between 64 and 8192")
	case s.DailyCallCap < 0 || s.DailyCallCap > 1_000_000:
		return errors.New("daily_call_cap must be between 0 (no limit) and 1000000")
	case s.ChatDailyCallCap < 0 || s.ChatDailyCallCap > 100_000 || s.SimulateDailyCap < 0 || s.SimulateDailyCap > 100_000:
		return errors.New("chat_daily_call_cap and simulate_daily_cap must be between 0 (no limit) and 100000")
	case bad(s.PriceInputPerMTok) || bad(s.PriceOutputPerMTok):
		return errors.New("prices must be between 0 and 1000 USD per million tokens")
	case len(sec.APIKey) > 1000 || len(sec.AudioAPIKey) > 1000:
		return errors.New("keys are limited to 1000 characters")
	case len(s.Audio.TranscriptionModel) > 200 || len(s.Audio.SpeechModel) > 200 || len(s.Audio.Voice) > 100:
		return errors.New("audio model names are limited to 200 characters")
	case (s.Audio.TranscriptionModel != "" || s.Audio.SpeechModel != "") && s.audioBaseURL() == "":
		return errors.New("audio.base_url is required for transcription or speech with the anthropic provider")
	}
	return nil
}

func bad(f float64) bool { return f < 0 || f > 1000 || math.IsNaN(f) }

// audioBaseURL is the audio server (the chat server for the openai provider).
func (s Settings) audioBaseURL() string {
	if s.Audio.BaseURL != "" {
		return s.Audio.BaseURL
	}
	if s.Provider == ProviderOpenAI {
		return s.BaseURL
	}
	return ""
}

// audioKey is the key of the audio server (the chat key when it is the same server).
func (s Settings) audioKey(sec Secrets) string {
	if sec.AudioAPIKey != "" {
		return sec.AudioAPIKey
	}
	if s.Provider == ProviderOpenAI && (s.Audio.BaseURL == "" || s.Audio.BaseURL == s.BaseURL) {
		return sec.APIKey
	}
	return ""
}

// ready reports why the settings cannot answer yet ("" = ready).
func (s Settings) ready(sec Secrets) string {
	if s.Provider == ProviderAnthropic && sec.APIKey == "" {
		return "no Anthropic API key configured"
	}
	if p, ok := presetByID(s.Preset); ok && p.NeedsKey && sec.APIKey == "" && sameBase(s.BaseURL, p.BaseURL) {
		return "no API key configured: get a free one at " + p.KeyURL
	}
	return ""
}

// builtinPrices are USD per million tokens (input, output) of known models.
var builtinPrices = map[string][2]float64{
	"claude-sonnet-5-5": {2, 10},
	"claude-sonnet-5":   {2, 10},
	"claude-opus-5-5":   {4, 20},
	"claude-opus-5":     {5, 25},
	"claude-haiku-5-5":  {0.10, 0.50},
	"claude-haiku-4-5":  {1, 5},
	"claude-sonnet-4-6": {3, 15},
}

// costMicros estimates a call's cost in millionths of USD.
func (s Settings) costMicros(model string, in, out int64) int64 {
	pi, po := s.PriceInputPerMTok, s.PriceOutputPerMTok
	if s.Free() {
		return 0
	}
	if pi == 0 && po == 0 {
		if p, ok := builtinPrices[model]; ok {
			pi, po = p[0], p[1]
		} else if p, ok := builtinPrices[s.Model]; ok {
			pi, po = p[0], p[1]
		}
	}
	return int64(math.Round(float64(in)*pi + float64(out)*po))
}

func (s Settings) String() string { return fmt.Sprintf("%s/%s", s.Provider, s.Model) }
