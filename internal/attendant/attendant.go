// Package attendant is the built-in attendant ("Atendente LiteZap", spec 4.7): agent
// profiles trained with the business context, LLM providers (Anthropic, OpenAI-compatible),
// voice-note transcription and voice replies. It plugs into the agent hook as its
// "builtin" target, so the hook's safety machinery (one call at a time per chat, human
// pause, loop guard, send queue with pacing) applies unchanged.
package attendant

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

// MediaSource opens a received attachment of an instance (service.Messaging).
type MediaSource interface {
	Media(ctx context.Context, instanceID, mediaID string) (service.MediaFile, error)
}

// Attendant answers agent hook requests with an LLM.
type Attendant struct {
	Store   *store.Store
	Vault   *Vault
	Media   MediaSource // nil: attachments are only named
	Factory Factory     // nil: DefaultFactory
	Clock   func() time.Time
	// Location is the instance's timezone (nil: UTC).
	Location func(ctx context.Context, instanceID string) *time.Location
	Log      *slog.Logger

	mu    sync.Mutex
	marks map[string]eventMark // instance|chat|type -> last emitted (dedupe)
}

type eventMark struct {
	hash string
	at   time.Time
}

var _ agenthook.BuiltinAgent = (*Attendant)(nil)

// Media limits (bytes).
const (
	maxImageBytes = 5 << 20
	maxPDFBytes   = 10 << 20
	maxAudioBytes = 16 << 20
	maxTextDoc    = 20 << 10
)

func (a *Attendant) now() time.Time {
	if a.Clock != nil {
		return a.Clock()
	}
	return time.Now()
}

func (a *Attendant) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (a *Attendant) loc(ctx context.Context, id string) *time.Location {
	if a.Location != nil {
		if l := a.Location(ctx, id); l != nil {
			return l
		}
	}
	return time.UTC
}

func notConfigured(detail string) error {
	return &agenthook.CallError{Reason: "not_configured", Detail: detail}
}

// load returns the instance's settings and decrypted secrets (ok false when unset).
func (a *Attendant) load(ctx context.Context, instanceID string) (Settings, Secrets, bool, error) {
	row, err := a.Store.GetAttendantSettings(ctx, instanceID)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultSettings(), Secrets{}, false, nil
	}
	if err != nil {
		return Settings{}, Secrets{}, false, err
	}
	s := DefaultSettings()
	if err := json.Unmarshal([]byte(row.SettingsJSON), &s); err != nil {
		return Settings{}, Secrets{}, false, fmt.Errorf("stored attendant settings: %w", err)
	}
	var sec Secrets
	if row.SecretsEnc != "" {
		pt, err := a.Vault.Open(instanceID, row.SecretsEnc)
		if err != nil {
			return Settings{}, Secrets{}, false, err
		}
		if err := json.Unmarshal(pt, &sec); err != nil {
			return Settings{}, Secrets{}, false, errors.New("stored attendant secrets are corrupt")
		}
	}
	return s, sec, true, nil
}

// activeProfile returns the agent that answers: the active one, else the first.
func (a *Attendant) activeProfile(ctx context.Context, instanceID string, s Settings) (string, Profile, error) {
	var row store.AttendantAgent
	var err error
	if s.ActiveAgentID != "" {
		row, err = a.Store.GetAttendantAgent(ctx, instanceID, s.ActiveAgentID)
	}
	if s.ActiveAgentID == "" || errors.Is(err, store.ErrNotFound) {
		list, lerr := a.Store.ListAttendantAgents(ctx, instanceID)
		if lerr != nil {
			return "", Profile{}, lerr
		}
		if len(list) == 0 {
			return "", Profile{}, notConfigured("no agent profile: create one in the attendant")
		}
		row, err = list[0], nil
	}
	if err != nil {
		return "", Profile{}, err
	}
	p, err := decodeProfile(row)
	return row.ID, p, err
}

func decodeProfile(row store.AttendantAgent) (Profile, error) {
	var p Profile
	if err := json.Unmarshal([]byte(row.ProfileJSON), &p); err != nil {
		return Profile{}, fmt.Errorf("stored agent profile: %w", err)
	}
	p.Name = row.Name
	p.normalize()
	return p, nil
}

// backends builds the provider clients.
func (a *Attendant) backends(s Settings, sec Secrets) (Backends, error) {
	f := a.Factory
	if f == nil {
		f = DefaultFactory
	}
	return f(s, sec)
}

// reserve counts one provider call against the daily cap.
func (a *Attendant) reserve(ctx context.Context, instanceID string, s Settings) error {
	ok, err := a.Store.ReserveAttendantCall(ctx, instanceID, a.now().UTC().Format("2006-01-02"), s.DailyCallCap)
	if err != nil {
		return err
	}
	if !ok {
		return &agenthook.CallError{Reason: "daily_cap", Detail: fmt.Sprintf("daily limit of %d provider calls reached (daily_call_cap)", s.DailyCallCap)}
	}
	return nil
}

func (a *Attendant) addUsage(instanceID string, s Settings, model string, in, out int64) int64 {
	cost := s.costMicros(model, in, out)
	if err := a.Store.AddAttendantTokens(context.Background(), instanceID, a.now().UTC().Format("2006-01-02"), in, out, cost); err != nil {
		a.log().Error("attendant: log usage", "instance_id", instanceID, "err", err)
	}
	return cost
}

// Answer is the agent hook's built-in target.
func (a *Attendant) Answer(ctx context.Context, req agenthook.Request) (agenthook.Response, error) {
	s, sec, ok, err := a.load(ctx, req.InstanceID)
	if err != nil {
		return agenthook.Response{}, err
	}
	if !ok {
		return agenthook.Response{}, notConfigured("the built-in attendant has no provider configured")
	}
	if why := s.ready(sec); why != "" {
		return agenthook.Response{}, notConfigured(why)
	}
	agentID, prof, err := a.activeProfile(ctx, req.InstanceID, s)
	if err != nil {
		return agenthook.Response{}, err
	}
	backs, err := a.backends(s, sec)
	if err != nil {
		return agenthook.Response{}, notConfigured(err.Error())
	}
	var msg events.Message
	_ = json.Unmarshal(req.Message, &msg)
	in := runInput{instanceID: req.InstanceID, chat: req.Chat.JID, agentID: agentID, profile: prof, settings: s,
		secrets: sec, backs: backs, trigger: req.Trigger, latestType: msg.Type}
	if req.Trigger == "call" {
		if !prof.CallFollowUp {
			return agenthook.Response{}, nil // the agent does not follow up on calls: stay silent
		}
		in.history, _ = a.lines(ctx, req, s, backs, msg.ID, &in)
	} else {
		in.history, in.latest = a.lines(ctx, req, s, backs, msg.ID, &in)
		if len(in.latest) == 0 {
			return agenthook.Response{}, nil // nothing new from the customer
		}
	}
	if !prof.HideContactName && !req.Chat.IsGroup {
		in.contactName = req.Chat.Name
	}
	res, err := a.run(ctx, in)
	if err != nil {
		return agenthook.Response{}, err
	}
	return res.response(), nil
}

// lines splits the context into history and the customer's latest messages (the trailing
// run of contact messages), enriching the triggering message's attachment.
func (a *Attendant) lines(ctx context.Context, req agenthook.Request, s Settings, backs Backends, msgID string, in *runInput) (history, latest []Line) {
	cms := req.Context
	if len(cms) == 0 && req.Trigger != "call" { // context_messages 0: only the message itself
		var m events.Message
		if json.Unmarshal(req.Message, &m) == nil {
			cms = []agenthook.ContextMessage{{ID: m.ID, Source: "contact", Origin: "contact", Type: m.Type,
				Text: m.Text, Media: m.Media, Timestamp: m.Timestamp}}
		}
	}
	split := len(cms)
	if req.Trigger != "call" {
		for split > 0 && cms[split-1].Origin == "contact" {
			split--
		}
	}
	for i, cm := range cms {
		if cm.Revoked {
			continue
		}
		origin := cm.Origin
		if origin == "" { // an older engine or a test request
			origin = agenthook.Origin(cm.FromMe, cm.Source, "")
		}
		text := describe(cm.Type, cm.Text, cm.Media)
		if i >= split && cm.ID == msgID && cm.Media != nil {
			text = a.enrich(ctx, req.InstanceID, s, backs, cm, text, in)
		}
		if text == "" {
			continue
		}
		l := Line{Origin: origin, At: cm.Timestamp, Text: text}
		if i >= split {
			latest = append(latest, l)
		} else {
			history = append(history, l)
		}
	}
	return history, latest
}

var typeLabels = map[string]string{"image": "imagem", "video": "vídeo", "audio": "áudio", "document": "documento",
	"sticker": "figurinha", "gif": "gif", "ptv": "vídeo", "location": "localização", "contact": "contato",
	"poll": "enquete", "reaction": "reação"}

// describe renders a message as text; attachments are named, not opened.
func describe(typ, text string, md *events.Media) string {
	label := typeLabels[typ]
	if typ == "audio" && md != nil && md.PTT {
		label = "mensagem de voz"
	}
	switch {
	case label != "" && text != "":
		return "[" + label + "] " + text
	case label != "":
		return "[" + label + "]"
	default:
		return text
	}
}

// enrich opens the triggering message's attachment: voice notes are transcribed, images
// and PDFs go to the model when it supports them, small text documents are inlined.
func (a *Attendant) enrich(ctx context.Context, instanceID string, s Settings, backs Backends, cm agenthook.ContextMessage, text string, in *runInput) string {
	md := cm.Media
	if a.Media == nil || md.ID == "" {
		return text
	}
	kind := cm.Type
	mime := baseMime(md.Mime)
	switch {
	case kind == "audio" && backs.Transcriber != nil:
		data, err := a.read(ctx, instanceID, md.ID, maxAudioBytes)
		if err != nil {
			return "[mensagem de voz — não foi possível ouvir]"
		}
		if err := a.reserve(ctx, instanceID, s); err != nil {
			return "[mensagem de voz — transcrição indisponível]"
		}
		got, err := backs.Transcriber.Transcribe(ctx, Blob{Mime: md.Mime, Name: audioName(mime), Data: data})
		if err != nil || got == "" {
			a.log().Warn("attendant: transcription failed", "instance_id", instanceID, "err", err)
			return "[mensagem de voz — não foi possível transcrever]"
		}
		in.transcribed = true
		return "[mensagem de voz, transcrição] " + got
	case (kind == "image") && s.Vision && backs.Chat.SupportsImage(mime) && md.Size <= maxImageBytes:
		data, err := a.read(ctx, instanceID, md.ID, maxImageBytes)
		if err != nil {
			return text
		}
		in.parts = append(in.parts, Part{Image: &Blob{Mime: mime, Data: data}})
		return text + " (imagem anexada abaixo)"
	case kind == "document" && s.Vision && backs.Chat.SupportsDocument(mime) && md.Size <= maxPDFBytes:
		data, err := a.read(ctx, instanceID, md.ID, maxPDFBytes)
		if err != nil {
			return text
		}
		in.parts = append(in.parts, Part{Document: &Blob{Mime: mime, Name: md.FileName, Data: data}})
		return text + " (documento anexado abaixo)"
	case kind == "document" && (mime == "text/plain" || mime == "text/csv") && md.Size <= maxTextDoc:
		data, err := a.read(ctx, instanceID, md.ID, maxTextDoc)
		if err != nil {
			return text
		}
		return text + " (conteúdo do arquivo): " + strings.ToValidUTF8(string(data), "")
	}
	return text
}

func audioName(mime string) string {
	switch mime {
	case "audio/mpeg":
		return "audio.mp3"
	case "audio/mp4", "audio/aac":
		return "audio.m4a"
	case "audio/wav", "audio/x-wav":
		return "audio.wav"
	}
	return "audio.ogg"
}

// read loads an attachment of the instance, refusing more than max bytes.
func (a *Attendant) read(ctx context.Context, instanceID, mediaID string, max int64) ([]byte, error) {
	f, err := a.Media.Media(ctx, instanceID, mediaID)
	if err != nil {
		return nil, err
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	data, err := io.ReadAll(io.LimitReader(fh, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("attachment too large")
	}
	return data, nil
}

// runInput is one answer to compute.
type runInput struct {
	instanceID, chat, agentID string
	profile                   Profile
	settings                  Settings
	secrets                   Secrets
	backs                     Backends
	history, latest           []Line
	parts                     []Part // attachments of the latest message
	contactName               string
	trigger                   string // "" | call
	latestType                string
	transcribed               bool
	simulate                  bool // no voice synthesis, no event dedupe
}

// runResult is a computed answer.
type runResult struct {
	Reply       string
	Voice       *Blob
	WouldVoice  bool
	Handoff     bool
	HandoffNote string
	Events      []agenthook.AgentEvent
	Model       string
	InputTok    int64
	OutputTok   int64
	CostMicros  int64
}

func (r runResult) response() agenthook.Response {
	var resp agenthook.Response
	switch {
	case r.Voice != nil:
		b, _ := json.Marshal(map[string]any{"type": "audio", "media": map[string]any{
			"base64": base64.StdEncoding.EncodeToString(r.Voice.Data), "mime": "audio/ogg", "ptt": true}})
		resp.Reply = b
	case r.Reply != "":
		resp.Reply, _ = json.Marshal(r.Reply)
	}
	if r.Handoff {
		a, _ := json.Marshal(map[string]string{"type": "handoff", "note": r.HandoffNote})
		resp.Actions = append(resp.Actions, a)
	}
	resp.Events = r.Events
	return resp
}

const callTask = `The customer just called this WhatsApp number. Calls are not answered here, so the call was declined automatically. Write ONE short, friendly message inviting them to say here how you can help (do not apologize at length, do not mention technical details). Use event.type "none".`

const messageTask = `Write your answer to the customer's new message as the JSON object described in your instructions.`

// run calls the model and turns its answer into a safe result.
func (a *Attendant) run(ctx context.Context, in runInput) (runResult, error) {
	p, s := in.profile, in.settings
	canary := newCanary()
	task := messageTask
	if in.trigger == "call" {
		task = callTask
		if p.CallHint != "" {
			task += "\nOperator's guidance for this message: " + p.CallHint
		}
	}
	turn := userTurn(in.history, in.latest, in.contactName, a.now(), a.loc(ctx, in.instanceID), task)
	parts := append([]Part{{Text: turn}}, in.parts...)
	if err := a.reserve(ctx, in.instanceID, s); err != nil {
		return runResult{}, err
	}
	resp, err := in.backs.Chat.Chat(ctx, ChatRequest{System: systemPrompt(p, canary), MaxTokens: s.MaxTokens,
		Messages: []ChatMessage{{Role: "user", Parts: parts}}, Schema: outputSchema,
		User: a.Vault.Pseudonym(in.instanceID, in.chat)})
	model := resp.Model
	if model == "" {
		model = s.Model
	}
	res := runResult{Model: model, InputTok: resp.InputTokens, OutputTok: resp.OutputTokens}
	if resp.InputTokens > 0 || resp.OutputTokens > 0 {
		res.CostMicros = a.addUsage(in.instanceID, s, model, resp.InputTokens, resp.OutputTokens)
	}
	if err != nil {
		if ctx.Err() != nil {
			return runResult{}, ctx.Err()
		}
		return runResult{}, &agenthook.CallError{Reason: "llm_error", Detail: err.Error()}
	}
	if resp.Refused {
		res.Handoff, res.HandoffNote = true, "the model declined to answer; please take over"
		res.Reply = "Vou chamar uma pessoa da nossa equipe para continuar o atendimento. Só um instante!"
		return res, nil
	}
	out, err := parseOutput(resp.Text)
	if err != nil {
		return runResult{}, &agenthook.CallError{Reason: "invalid_response", Detail: err.Error()}
	}
	reply := strings.TrimSpace(out.Reply)
	if leaks(reply, canary, in.secrets) {
		return runResult{}, &agenthook.CallError{Reason: "invalid_response",
			Detail: "reply blocked: it contained the agent's instructions or keys"}
	}
	res.Reply = clip(reply, p.MaxReplyChars)
	if out.Handoff {
		res.Handoff, res.HandoffNote = true, clip(strings.TrimSpace(out.HandoffNote), 300)
		if res.HandoffNote == "" {
			res.HandoffNote = "the agent asked for a human"
		}
	}
	if in.trigger != "call" {
		res.Events = a.buildEvents(in, out.Event)
	}
	if res.Reply != "" && in.backs.Speaker != nil &&
		(p.VoiceReply == VoiceAlways || (p.VoiceReply == VoiceWhenAudio && in.latestType == "audio")) {
		res.WouldVoice = true
		if !in.simulate {
			res.Voice = a.speak(ctx, in, res.Reply)
		}
	}
	return res, nil
}

// speak synthesizes the reply; nil (a text reply) when it fails or the cap is reached.
func (a *Attendant) speak(ctx context.Context, in runInput, text string) *Blob {
	if err := a.reserve(ctx, in.instanceID, in.settings); err != nil {
		return nil
	}
	b, err := in.backs.Speaker.Speak(ctx, text)
	if err != nil {
		a.log().Warn("attendant: speech failed; replying with text", "instance_id", in.instanceID, "err", err)
		return nil
	}
	return &b
}

// leaks reports a reply that echoes the system prompt or a key.
func leaks(reply, canary string, sec Secrets) bool {
	if reply == "" {
		return false
	}
	if strings.Contains(reply, canary) || strings.Contains(reply, "Internal marker") || strings.Contains(reply, "# Security rules") {
		return true
	}
	for _, k := range []string{sec.APIKey, sec.AudioAPIKey} {
		if len(k) >= 8 && strings.Contains(reply, k) {
			return true
		}
	}
	return false
}

// events validates the model's event against the agent's allowed actions and builds it
// (the engine recomputes order totals from the catalog).
func (a *Attendant) buildEvents(in runInput, e modelEvent) []agenthook.AgentEvent {
	p := in.profile
	base := map[string]any{"agent_id": in.agentID, "agent_name": p.Name, "mode": p.Mode}
	str := func(s string, n int) string { return clip(strings.TrimSpace(s), n) }
	var typ string
	data := base
	switch e.Type {
	case "lead":
		if !p.allows(ActionLead) || (str(e.Need, 1000) == "" && str(e.CustomerName, 200) == "") {
			return nil
		}
		typ = events.TypeAgentLead
		data["customer_name"], data["need"], data["notes"] = str(e.CustomerName, 200), str(e.Need, 1000), str(e.Notes, 1000)
	case "appointment_requested":
		if !p.allows(ActionAppointment) || str(e.PreferredTime, 200) == "" {
			return nil
		}
		typ = events.TypeAgentAppointment
		data["customer_name"], data["need"] = str(e.CustomerName, 200), str(e.Need, 1000)
		data["preferred_time"], data["notes"] = str(e.PreferredTime, 200), str(e.Notes, 1000)
	case "order_draft":
		if !p.allows(ActionOrderDraft) {
			return nil
		}
		items, total, allCatalog := priceOrder(p, e.Items)
		if len(items) == 0 {
			return nil
		}
		typ = events.TypeAgentOrderDraft
		data["items"], data["total"], data["currency"] = items, total, p.Currency
		data["all_items_in_catalog"] = allCatalog
		data["address"], data["notes"], data["customer_name"] = str(e.Address, 500), str(e.Notes, 1000), str(e.CustomerName, 200)
	default:
		return nil
	}
	if !in.simulate && a.duplicate(in.instanceID, in.chat, typ, data) {
		return nil
	}
	return []agenthook.AgentEvent{{Type: typ, Data: data}}
}

// priceOrder keeps sane items, taking unit prices from the catalog when the name matches.
func priceOrder(p Profile, raw []orderItem) ([]map[string]any, float64, bool) {
	catalog := map[string]float64{}
	for _, k := range p.Knowledge {
		if k.Kind == "product" && k.Price != nil {
			catalog[strings.ToLower(strings.TrimSpace(k.Title))] = *k.Price
		}
	}
	var items []map[string]any
	total, all := 0.0, true
	for _, it := range raw {
		name := clip(strings.TrimSpace(it.Name), 200)
		if name == "" || it.Quantity <= 0 || it.Quantity > 10000 || it.UnitPrice < 0 || it.UnitPrice > 1e9 || len(items) >= 100 {
			continue
		}
		price, inCatalog := catalog[strings.ToLower(name)]
		if !inCatalog {
			price, all = it.UnitPrice, false
		}
		sub := price * it.Quantity
		total += sub
		items = append(items, map[string]any{"name": name, "quantity": it.Quantity, "unit_price": price,
			"subtotal": roundMoney(sub), "in_catalog": inCatalog})
	}
	return items, roundMoney(total), all
}

func roundMoney(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// duplicate drops an event identical to the last one of its type in the chat (24h).
func (a *Attendant) duplicate(instanceID, chat, typ string, data map[string]any) bool {
	b, _ := json.Marshal(data)
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	key := instanceID + "|" + chat + "|" + typ
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.marks == nil {
		a.marks = map[string]eventMark{}
	}
	if len(a.marks) > 10000 {
		for k, m := range a.marks {
			if now.Sub(m.at) > 24*time.Hour {
				delete(a.marks, k)
			}
		}
	}
	if m, ok := a.marks[key]; ok && m.hash == h && now.Sub(m.at) < 24*time.Hour {
		return true
	}
	a.marks[key] = eventMark{hash: h, at: now}
	return false
}
