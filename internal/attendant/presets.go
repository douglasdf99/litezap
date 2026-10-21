package attendant

import (
	"net"
	"net/url"
	"strings"
)

// Free provider presets: named defaults over the openai provider (any OpenAI-compatible
// server). They exist so a business without money or a credit card can use the attendant
// and the agents: a local model (Ollama) or a free cloud tier (Google Gemini, Groq).

// Preset ids.
const (
	PresetOllama     = "ollama"
	PresetGeminiFree = "gemini_free"
	PresetGroqFree   = "groq_free"
)

// OllamaBaseURL is the loopback Ollama server (OpenAI-compatible API).
const OllamaBaseURL = "http://127.0.0.1:11434/v1"

// ProviderPreset describes one free provider option.
type ProviderPreset struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider"` // always openai
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	// Models are alternatives the UI offers (the first is the default).
	Models []string `json:"models"`
	// NeedsKey: a (free) API key is required; KeyURL is where to get it.
	NeedsKey bool   `json:"needs_key"`
	KeyURL   string `json:"key_url,omitempty"`
	Local    bool   `json:"local"`
	Free     bool   `json:"free"`
	// Quality and Speed are honest one-line expectations, in Portuguese.
	Quality string `json:"quality"`
	Speed   string `json:"speed"`
	Privacy string `json:"privacy"`
	// noUser: the server rejects the OpenAI "user" field.
	noUser bool
}

// providerPresets is the built-in table (checked October 2026: qwen2.5:7b and
// llama3.2:3b support tools on Ollama; gemini-2.5-flash is in Gemini's free tier;
// llama-3.3-70b-versatile is a production, tool-capable model on Groq).
var providerPresets = []ProviderPreset{
	{ID: PresetOllama, Label: "Grátis no meu computador (Ollama)", Provider: ProviderOpenAI, BaseURL: OllamaBaseURL,
		Model: "qwen2.5:7b", Models: []string{"qwen2.5:7b", "llama3.2:3b"}, Local: true, Free: true,
		Quality: "Boa para atendimento simples em português; erra mais que os modelos pagos em tarefas longas.",
		Speed:   "Depende do computador: de 2 a 20 segundos por resposta (mais rápido com placa de vídeo ou Mac M1+).",
		Privacy: "As conversas não saem do seu computador."},
	{ID: PresetGeminiFree, Label: "Grátis na nuvem (Google Gemini)", Provider: ProviderOpenAI,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Model: "gemini-2.5-flash",
		Models: []string{"gemini-2.5-flash", "gemini-2.5-flash-lite"}, NeedsKey: true, KeyURL: "https://aistudio.google.com/apikey",
		Free: true, noUser: true,
		Quality: "Muito boa, perto dos modelos pagos.",
		Speed:   "Rápida (1 a 5 segundos), com limite de pedidos por minuto e por dia no plano grátis.",
		Privacy: "No plano grátis o Google pode usar as conversas para melhorar os modelos e pessoas podem revisá-las."},
	{ID: PresetGroqFree, Label: "Groq grátis", Provider: ProviderOpenAI, BaseURL: "https://api.groq.com/openai/v1",
		Model: "llama-3.3-70b-versatile", Models: []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant"},
		NeedsKey: true, KeyURL: "https://console.groq.com/keys", Free: true,
		Quality: "Boa; o modelo de 70B segue bem as instruções e usa ferramentas.",
		Speed:   "Muito rápida (menos de 2 segundos), com limite de pedidos e de tokens por minuto no plano grátis.",
		Privacy: "As conversas passam pelos servidores da Groq (EUA)."},
}

// ProviderPresets returns the free provider presets.
func ProviderPresets() []ProviderPreset {
	out := make([]ProviderPreset, len(providerPresets))
	copy(out, providerPresets)
	for i := range out {
		out[i].Models = append([]string(nil), out[i].Models...)
	}
	return out
}

// presetByID finds a preset.
func presetByID(id string) (ProviderPreset, bool) {
	for _, p := range providerPresets {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderPreset{}, false
}

// isLoopbackURL reports a URL whose host is a loopback address or localhost.
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameBase compares base URLs ignoring a trailing slash and case of the host.
func sameBase(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}

// Free reports a configuration that costs nothing: a free preset pointing at its own
// server, or any local (loopback) OpenAI-compatible server, without operator prices.
func (s Settings) Free() bool {
	if s.PriceInputPerMTok != 0 || s.PriceOutputPerMTok != 0 || s.Provider != ProviderOpenAI {
		return false
	}
	if p, ok := presetByID(s.Preset); ok && p.Free && sameBase(s.BaseURL, p.BaseURL) {
		return true
	}
	return isLoopbackURL(s.BaseURL)
}

// LocalModel reports a small local model: prompts are shortened for it.
func (s Settings) LocalModel() bool {
	return s.Provider == ProviderOpenAI && (s.Preset == PresetOllama || isLoopbackURL(s.BaseURL))
}

// sendsUser reports whether the server accepts the OpenAI "user" field.
func (s Settings) sendsUser() bool {
	if p, ok := presetByID(s.Preset); ok && p.noUser && sameBase(s.BaseURL, p.BaseURL) {
		return false
	}
	return !strings.Contains(strings.ToLower(s.BaseURL), "generativelanguage.googleapis.com")
}
