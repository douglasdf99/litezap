package attendant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

// maxAgents bounds agent profiles per instance.
const maxAgents = 20

// SettingsView is the settings as the API shows them: keys never come back.
type SettingsView struct {
	Configured     bool      `json:"configured"`
	Settings                 // embedded fields
	APIKeySet      bool      `json:"api_key_set"`
	AudioAPIKeySet bool      `json:"audio_api_key_set"`
	Ready          bool      `json:"ready"`
	NotReady       string    `json:"not_ready,omitempty"`
	Transcription  bool      `json:"transcription"`
	Speech         bool      `json:"speech"`
	Today          UsageView `json:"today"`
}

// UsageView is one day of usage.
type UsageView struct {
	Day          string  `json:"day"`
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// AgentView is a stored agent.
type AgentView struct {
	ID        string    `json:"id"`
	Active    bool      `json:"active"`
	Profile             // name, mode, script...
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Admin holds the attendant use cases of the REST API and MCP.
type Admin struct {
	A *Attendant

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (ad *Admin) lock(id string) func() {
	ad.mu.Lock()
	if ad.locks == nil {
		ad.locks = map[string]*sync.Mutex{}
	}
	l := ad.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		ad.locks[id] = l
	}
	ad.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (ad *Admin) instance(ctx context.Context, id string) error {
	if _, err := ad.A.Store.GetInstance(ctx, id); errors.Is(err, store.ErrNotFound) {
		return apierr.InstanceNotFound()
	} else if err != nil {
		return err
	}
	return nil
}

func usageView(u store.AttendantUsage) UsageView {
	return UsageView{Day: u.Day, Calls: u.Calls, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CostUSD: float64(u.CostMicros) / 1e6}
}

func (ad *Admin) view(ctx context.Context, id string, s Settings, sec Secrets, configured bool) SettingsView {
	v := SettingsView{Configured: configured, Settings: s, APIKeySet: sec.APIKey != "", AudioAPIKeySet: sec.AudioAPIKey != "",
		Transcription: s.Audio.TranscriptionModel != "" && s.audioBaseURL() != "",
		Speech:        s.Audio.SpeechModel != "" && s.audioBaseURL() != ""}
	v.NotReady = s.ready(sec)
	if !configured {
		v.NotReady = "not configured"
	}
	v.Ready = v.NotReady == ""
	day := ad.A.now().UTC().Format("2006-01-02")
	v.Today = UsageView{Day: day}
	if list, err := ad.A.Store.ListAttendantUsage(ctx, id, day); err == nil && len(list) > 0 {
		v.Today = usageView(list[0])
	}
	return v
}

// GetSettings returns the settings (defaults with configured=false when unset).
func (ad *Admin) GetSettings(ctx context.Context, id string) (SettingsView, error) {
	if err := ad.instance(ctx, id); err != nil {
		return SettingsView{}, err
	}
	s, sec, ok, err := ad.A.load(ctx, id)
	if err != nil {
		return SettingsView{}, err
	}
	return ad.view(ctx, id, s, sec, ok), nil
}

// PutSettings applies a patch (admin only: it chooses the provider and holds the keys).
func (ad *Admin) PutSettings(ctx context.Context, id string, p SettingsPatch) (SettingsView, error) {
	if err := ad.instance(ctx, id); err != nil {
		return SettingsView{}, err
	}
	defer ad.lock(id)()
	cur, sec, _, err := ad.A.load(ctx, id)
	if err != nil {
		return SettingsView{}, err
	}
	s, sec := p.apply(cur, sec)
	if p.Provider != nil && *p.Provider != cur.Provider && p.Model == nil {
		// A new provider does not keep the old provider's model.
		s.Model = ""
		if s.Provider == ProviderAnthropic {
			s.Model = DefaultModel
		}
	}
	if err := s.Validate(sec); err != nil {
		return SettingsView{}, apierr.Validation(err.Error())
	}
	if err := ad.save(ctx, id, s, sec); err != nil {
		return SettingsView{}, err
	}
	return ad.view(ctx, id, s, sec, true), nil
}

func (ad *Admin) save(ctx context.Context, id string, s Settings, sec Secrets) error {
	js, _ := json.Marshal(s)
	enc := ""
	if sec != (Secrets{}) {
		b, _ := json.Marshal(sec)
		var err error
		if enc, err = ad.A.Vault.Seal(id, b); err != nil {
			return err
		}
	}
	return ad.A.Store.PutAttendantSettings(ctx, id, store.AttendantSettings{SettingsJSON: string(js), SecretsEnc: enc})
}

// Presets returns the editable presets of each mode.
func (ad *Admin) Presets() map[string]Profile {
	out := Presets()
	for k, p := range out {
		p.normalize()
		p.Name = ""
		out[k] = p
	}
	return out
}

func agentView(row store.AttendantAgent, activeID string) (AgentView, error) {
	p, err := decodeProfile(row)
	if err != nil {
		return AgentView{}, err
	}
	return AgentView{ID: row.ID, Active: row.ID == activeID, Profile: p, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

// activeID is the agent that answers: the configured one, else the first.
func (ad *Admin) activeID(ctx context.Context, id string, rows []store.AttendantAgent) string {
	s, _, _, err := ad.A.load(ctx, id)
	if err == nil && s.ActiveAgentID != "" {
		for _, r := range rows {
			if r.ID == s.ActiveAgentID {
				return r.ID
			}
		}
	}
	if len(rows) > 0 {
		return rows[0].ID
	}
	return ""
}

// ListAgents returns the instance's agents.
func (ad *Admin) ListAgents(ctx context.Context, id string) ([]AgentView, error) {
	if err := ad.instance(ctx, id); err != nil {
		return nil, err
	}
	rows, err := ad.A.Store.ListAttendantAgents(ctx, id)
	if err != nil {
		return nil, err
	}
	active := ad.activeID(ctx, id, rows)
	out := make([]AgentView, 0, len(rows))
	for _, r := range rows {
		v, err := agentView(r, active)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func agentNotFound() error {
	return apierr.New(http.StatusNotFound, "not_found", "agent not found", false)
}

// GetAgent returns one agent.
func (ad *Admin) GetAgent(ctx context.Context, id, agentID string) (AgentView, error) {
	if err := ad.instance(ctx, id); err != nil {
		return AgentView{}, err
	}
	rows, err := ad.A.Store.ListAttendantAgents(ctx, id)
	if err != nil {
		return AgentView{}, err
	}
	for _, r := range rows {
		if r.ID == agentID {
			return agentView(r, ad.activeID(ctx, id, rows))
		}
	}
	return AgentView{}, agentNotFound()
}

// prepare fills a new profile from its mode's preset and validates it.
func prepare(p Profile, fillPreset bool) (Profile, error) {
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if fillPreset {
		p = withPreset(p)
	}
	p.normalize()
	if err := p.Validate(); err != nil {
		return Profile{}, apierr.Validation(err.Error())
	}
	return p, nil
}

func storedProfile(p Profile) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	if len(b) > 256<<10 {
		return "", apierr.Validation("the agent profile is larger than 256KB")
	}
	return string(b), nil
}

// CreateAgent stores a new agent; empty script fields come from the mode's preset.
func (ad *Admin) CreateAgent(ctx context.Context, id string, p Profile) (AgentView, error) {
	if err := ad.instance(ctx, id); err != nil {
		return AgentView{}, err
	}
	p, err := prepare(p, true)
	if err != nil {
		return AgentView{}, err
	}
	js, err := storedProfile(p)
	if err != nil {
		return AgentView{}, err
	}
	defer ad.lock(id)()
	if n, err := ad.A.Store.CountAttendantAgents(ctx, id); err != nil {
		return AgentView{}, err
	} else if n >= maxAgents {
		return AgentView{}, apierr.Validation("at most 20 agents per instance")
	}
	row := store.AttendantAgent{ID: secure.NewID("agt_"), InstanceID: id, Name: p.Name, ProfileJSON: js}
	if err := ad.A.Store.CreateAttendantAgent(ctx, row); err != nil {
		return AgentView{}, err
	}
	return ad.GetAgent(ctx, id, row.ID)
}

// UpdateAgent replaces an agent's profile.
func (ad *Admin) UpdateAgent(ctx context.Context, id, agentID string, p Profile) (AgentView, error) {
	if err := ad.instance(ctx, id); err != nil {
		return AgentView{}, err
	}
	p, err := prepare(p, false)
	if err != nil {
		return AgentView{}, err
	}
	js, err := storedProfile(p)
	if err != nil {
		return AgentView{}, err
	}
	err = ad.A.Store.UpdateAttendantAgent(ctx, store.AttendantAgent{ID: agentID, InstanceID: id, Name: p.Name, ProfileJSON: js})
	if errors.Is(err, store.ErrNotFound) {
		return AgentView{}, agentNotFound()
	} else if err != nil {
		return AgentView{}, err
	}
	return ad.GetAgent(ctx, id, agentID)
}

// DeleteAgent removes an agent.
func (ad *Admin) DeleteAgent(ctx context.Context, id, agentID string) error {
	if err := ad.instance(ctx, id); err != nil {
		return err
	}
	defer ad.lock(id)()
	// The answering agent is never swapped silently: activate another one first.
	if rows, err := ad.A.Store.ListAttendantAgents(ctx, id); err != nil {
		return err
	} else if len(rows) > 1 && ad.activeID(ctx, id, rows) == agentID {
		return apierr.New(http.StatusConflict, "agent_active", "this agent answers the chats: activate another agent before deleting it", false)
	}
	err := ad.A.Store.DeleteAttendantAgent(ctx, id, agentID)
	if errors.Is(err, store.ErrNotFound) {
		return agentNotFound()
	}
	return err
}

// ActivateAgent makes an agent the one that answers.
func (ad *Admin) ActivateAgent(ctx context.Context, id, agentID string) (AgentView, error) {
	if err := ad.instance(ctx, id); err != nil {
		return AgentView{}, err
	}
	if _, err := ad.A.Store.GetAttendantAgent(ctx, id, agentID); errors.Is(err, store.ErrNotFound) {
		return AgentView{}, agentNotFound()
	} else if err != nil {
		return AgentView{}, err
	}
	unlock := ad.lock(id)
	s, sec, _, err := ad.A.load(ctx, id)
	if err == nil {
		s.ActiveAgentID = agentID
		err = ad.save(ctx, id, s, sec)
	}
	unlock()
	if err != nil {
		return AgentView{}, err
	}
	return ad.GetAgent(ctx, id, agentID)
}

// Usage returns the last days of usage (default 7, at most 90), newest first.
func (ad *Admin) Usage(ctx context.Context, id string, days int) ([]UsageView, error) {
	if err := ad.instance(ctx, id); err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 7
	}
	days = min(days, 90)
	from := ad.A.now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	list, err := ad.A.Store.ListAttendantUsage(ctx, id, from)
	if err != nil {
		return nil, err
	}
	out := make([]UsageView, 0, len(list))
	for _, u := range list {
		out = append(out, usageView(u))
	}
	return out, nil
}

// SimMessage is one message of a simulated conversation.
type SimMessage struct {
	// From: customer (default) | agent | human | automatic.
	From string `json:"from"`
	Text string `json:"text"`
}

// SimulateInput runs an agent on a sample conversation; nothing is sent.
type SimulateInput struct {
	// AgentID picks a stored agent; Agent tests an unsaved profile instead (it wins).
	AgentID  string       `json:"agent_id,omitempty"`
	Agent    *Profile     `json:"agent,omitempty"`
	Messages []SimMessage `json:"messages"`
	// Trigger "call" simulates the follow-up after a rejected call.
	Trigger     string `json:"trigger,omitempty"`
	ContactName string `json:"contact_name,omitempty"`
}

// SimulateResult is what the agent would do.
type SimulateResult struct {
	Reply       string           `json:"reply"`
	Voice       bool             `json:"voice"` // would answer with a voice note
	Handoff     bool             `json:"handoff"`
	HandoffNote string           `json:"handoff_note,omitempty"`
	Events      []map[string]any `json:"events"`
	Model       string           `json:"model"`
	Usage       UsageView        `json:"usage"`
}

var simOrigins = map[string]string{"customer": "contact", "": "contact", "cliente": "contact", "agent": "agent",
	"human": "human", "automatic": "automatic"}

// Simulate runs an agent against sample messages without sending anything (it is a real
// provider call and counts against the daily cap).
func (ad *Admin) Simulate(ctx context.Context, id string, in SimulateInput) (SimulateResult, error) {
	if err := ad.instance(ctx, id); err != nil {
		return SimulateResult{}, err
	}
	if len(in.Messages) == 0 && in.Trigger != "call" {
		return SimulateResult{}, apierr.Validation("messages must have at least one message")
	}
	if len(in.Messages) > 50 {
		return SimulateResult{}, apierr.Validation("at most 50 messages")
	}
	if in.Trigger != "" && in.Trigger != "call" {
		return SimulateResult{}, apierr.Validation(`trigger must be empty or "call"`)
	}
	s, sec, ok, err := ad.A.load(ctx, id)
	if err != nil {
		return SimulateResult{}, err
	}
	if !ok {
		return SimulateResult{}, apierr.New(http.StatusConflict, "not_configured", "configure the attendant provider first", false)
	}
	if why := s.ready(sec); why != "" {
		return SimulateResult{}, apierr.New(http.StatusConflict, "not_configured", why, false)
	}
	var agentID string
	var prof Profile
	switch {
	case in.Agent != nil:
		if prof, err = prepare(*in.Agent, true); err != nil {
			return SimulateResult{}, err
		}
		agentID = "unsaved"
	case in.AgentID != "":
		row, err := ad.A.Store.GetAttendantAgent(ctx, id, in.AgentID)
		if errors.Is(err, store.ErrNotFound) {
			return SimulateResult{}, agentNotFound()
		} else if err != nil {
			return SimulateResult{}, err
		}
		if prof, err = decodeProfile(row); err != nil {
			return SimulateResult{}, err
		}
		agentID = row.ID
	default:
		if agentID, prof, err = ad.A.activeProfile(ctx, id, s); err != nil {
			return SimulateResult{}, apierr.New(http.StatusConflict, "not_configured", err.Error(), false)
		}
	}
	backs, err := ad.A.backends(s, sec)
	if err != nil {
		return SimulateResult{}, apierr.New(http.StatusConflict, "not_configured", err.Error(), false)
	}
	now := ad.A.now()
	var lines []Line
	for i, m := range in.Messages {
		origin, ok := simOrigins[strings.ToLower(strings.TrimSpace(m.From))]
		if !ok {
			return SimulateResult{}, apierr.Validation("from must be customer, agent, human or automatic")
		}
		if t := strings.TrimSpace(m.Text); t != "" {
			if len(t) > 4000 {
				return SimulateResult{}, apierr.Validation("each message is limited to 4000 characters")
			}
			lines = append(lines, Line{Origin: origin, At: now.Add(time.Duration(i-len(in.Messages)) * time.Minute), Text: t})
		}
	}
	split := len(lines)
	if in.Trigger != "call" {
		for split > 0 && lines[split-1].Origin == "contact" {
			split--
		}
		if split == len(lines) {
			return SimulateResult{}, apierr.Validation("the last message must be from the customer")
		}
	}
	if !prof.CallFollowUp && in.Trigger == "call" {
		return SimulateResult{Events: []map[string]any{}, Reply: ""}, nil
	}
	run := runInput{instanceID: id, chat: "simulation", agentID: agentID, profile: prof, settings: s, secrets: sec,
		backs: backs, history: lines[:split], latest: lines[split:], trigger: in.Trigger, simulate: true}
	if !prof.HideContactName {
		run.contactName = clip(strings.TrimSpace(in.ContactName), 100)
	}
	res, err := ad.A.run(ctx, run)
	if err != nil {
		var ce *agenthook.CallError
		if errors.As(err, &ce) && ce.Reason == "daily_cap" {
			return SimulateResult{}, apierr.New(http.StatusTooManyRequests, "daily_cap", ce.Detail, true)
		}
		return SimulateResult{}, apierr.New(http.StatusBadGateway, "simulation_failed", err.Error(), true)
	}
	out := SimulateResult{Reply: res.Reply, Voice: res.WouldVoice, Handoff: res.Handoff, HandoffNote: res.HandoffNote,
		Events: []map[string]any{}, Model: res.Model,
		Usage: UsageView{InputTokens: res.InputTok, OutputTokens: res.OutputTok, CostUSD: float64(res.CostMicros) / 1e6}}
	for _, e := range res.Events {
		d := map[string]any{"type": e.Type}
		for k, v := range e.Data {
			d[k] = v
		}
		out.Events = append(out.Events, d)
	}
	return out, nil
}
