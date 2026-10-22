// Package agents implements the LiteZap agents (spec 2026-10-20, slice A1): Claude
// agents that run a tool-use loop over LiteZap's own WhatsApp tools (the MCP tool table,
// executed in process), within permission groups, a mode (aprovacao, autonomo, sugerir)
// and limits enforced by the executor. Messages trigger them through the agent hook
// (target "agents"), which keeps its guards: one run at a time per chat, human pause,
// loop guard, ignored chats, stale messages and the operator's suspension. Every send
// goes through the guarded send queue.
package agents

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/service"
)

// Modes.
const (
	// ModeApproval: write tools become drafts that a human approves (the default).
	ModeApproval = "aprovacao"
	// ModeAutonomous: write tools run directly (still through the send queue).
	ModeAutonomous = "autonomo"
	// ModeSuggest: write tools are recorded as notes, never executed.
	ModeSuggest = "sugerir"
)

// Permission groups.
const (
	GroupRead       = "ler"
	GroupReadOther  = "ler_outros_chats"
	GroupReply      = "responder_no_chat"
	GroupOtherChats = "enviar_outros_chats"
	GroupLabels     = "etiquetas"
	GroupGroups     = "grupos"
	GroupMedia      = "midia"
)

// Groups lists the permission groups in display order.
var Groups = []string{GroupRead, GroupReadOther, GroupReply, GroupOtherChats, GroupLabels, GroupGroups, GroupMedia}

// RiskyGroups need an explicit confirmation in the dashboard.
var RiskyGroups = []string{GroupReadOther, GroupOtherChats, GroupGroups}

// groupTools maps each group to the MCP tools it grants. With ler only, the per-chat
// reads (messages, contact, group, message status) are limited to the trigger chat and
// list_chats returns metadata only (no message text); ler_outros_chats lifts that and adds
// the address book. enviar_outros_chats lifts the trigger-chat restriction of every send
// tool the agent has. label_chat (etiquetas) may label any chat.
var groupTools = map[string][]string{
	GroupRead: {"get_instance_status", "list_chats", "get_messages", "get_message_status", "get_contact",
		"list_groups", "get_group", "list_labels", "get_catalog", "get_product", "get_menu", "get_menu_item", "search_menu", "get_collection"},
	GroupReadOther: {"get_instance_status", "list_chats", "get_messages", "get_message_status", "get_contact",
		"list_groups", "get_group", "list_labels", "list_contacts", "get_catalog", "get_product", "get_menu", "get_menu_item",
		"search_menu", "get_collection"},
	// send_menu and send_collection (the Cardápio as text) and send_order_summary (the
	// cart priced by the menu) are replies; each of their messages counts as a send.
	// send_collection's photos need midia too.
	GroupReply:      {"send_message", "send_reaction", "send_menu", "send_collection", "send_order_summary"},
	GroupOtherChats: {"send_message", "send_reaction", "send_menu", "send_collection", "send_order_summary"},
	GroupLabels:     {"label_chat"},
	GroupGroups:     {"create_group", "update_group", "manage_group_participants", "get_group_invite_link"},
	// send_product (marco 12) carries the product photo: it needs midia, and like every
	// send it is limited to the trigger chat unless enviar_outros_chats.
	// send_menu_item carries the item's photo, so it needs midia too.
	GroupMedia: {"send_media", "send_product", "send_menu_item"},
}

// writeTools change something outside LiteZap: in aprovacao they become drafts, in
// sugerir notes, in the simulator dry runs.
// get_group_invite_link only reads, but the link lets anyone join: it is handled as a write.
var writeTools = map[string]bool{"send_message": true, "send_reaction": true, "send_media": true, "send_product": true,
	"send_menu": true, "send_menu_item": true, "send_collection": true, "send_order_summary": true, "label_chat": true,
	"create_group": true, "update_group": true, "manage_group_participants": true, "get_group_invite_link": true}

// groupChangeTools change a group (they count in limits.max_group_changes).
var groupChangeTools = map[string]bool{"create_group": true, "update_group": true, "manage_group_participants": true}

// chatReadTools read one chat; key is the argument naming it.
var chatReadTools = map[string]string{"get_messages": "chat", "get_contact": "contact", "get_group": "group"}

// sendTools send a message to their "to" (restricted to the trigger chat unless
// enviar_outros_chats).
var sendTools = map[string]bool{"send_message": true, "send_reaction": true, "send_media": true, "send_product": true,
	"send_menu": true, "send_menu_item": true, "send_collection": true, "send_order_summary": true}

// Trigger types.
const (
	TriggerMessage = "message" // a new message received (through the agent hook)
	TriggerManual  = "manual"  // only "Executar agora" / POST .../run
)

// Trigger says when an agent runs.
type Trigger struct {
	Type string `json:"type"`
	// Chats, when set, limits the agent to these chats (JIDs or phone numbers).
	Chats []string `json:"chats"`
	// Labels, when set, limits the agent to chats with one of these WhatsApp Business
	// labels (id or name).
	Labels []string `json:"labels"`
	// Keywords, when set, runs the agent only when the new message contains one of them
	// (case-insensitive).
	Keywords []string `json:"keywords"`
	// Groups also runs the agent in group chats (off by default: only 1:1 chats).
	Groups bool `json:"groups"`
}

// Limits bound one agent.
type Limits struct {
	// MaxSteps is the maximum model calls per run (default 8).
	MaxSteps int `json:"max_steps"`
	// MaxRecipients is the maximum distinct chats a run may send to (default 3).
	MaxRecipients int `json:"max_recipients"`
	// MaxSends is the maximum sends per run, in any chat, executed or drafted (default 5).
	MaxSends int `json:"max_sends"`
	// MaxWrites is the maximum write calls per run (sends, labels, group changes, invite
	// links), executed, drafted or suggested (default 10).
	MaxWrites int `json:"max_writes"`
	// MaxGroupChanges is the maximum group changes per run (default 3).
	MaxGroupChanges int `json:"max_group_changes"`
	// DailyRuns is the maximum runs per UTC day (default 200; 0 = no agent-level limit, the
	// provider caps still apply).
	DailyRuns int `json:"daily_runs"`
}

// Defaults.
const (
	DefaultMaxSteps      = 8
	DefaultMaxRecipients = 3
	DefaultMaxSends      = 5
	DefaultMaxWrites     = 10
	DefaultGroupChanges  = 3
	DefaultDailyRuns     = 200
	maxAgents            = 20
	maxInstructions      = 20000
	maxNameLen           = 80
	maxFilterItems       = 200
)

// Agent is an agent's configuration.
type Agent struct {
	ID           string
	Name         string
	Enabled      bool
	Mode         string
	Instructions string
	Tools        []string // permission groups
	Trigger      Trigger
	Limits       Limits
	// Runtime is who thinks: RuntimeProvider (the instance's AI provider, the default) or
	// RuntimeCodexCLI (the user's own Codex CLI, see cliruntime.go).
	Runtime   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Input creates or changes an agent; nil fields keep their value (or take the default on
// creation).
type Input struct {
	Name         *string  `json:"name"`
	Enabled      *bool    `json:"enabled"`
	Mode         *string  `json:"mode"`
	Instructions *string  `json:"instructions"`
	Tools        []string `json:"tools"`
	Trigger      *Trigger `json:"trigger"`
	Limits       *Limits  `json:"limits"`
	Runtime      *string  `json:"runtime"`
	// AcceptRuntimeNotice confirms the notice of an opt-in runtime (codex_cli): required
	// when an agent switches to it.
	AcceptRuntimeNotice *bool `json:"accept_runtime_notice"`
}

// defaultAgent is the starting point of a new agent: read + reply in the trigger chat,
// approval mode, new 1:1 messages, off until enabled.
func defaultAgent() Agent {
	return Agent{Mode: ModeApproval, Tools: []string{GroupRead, GroupReply}, Runtime: RuntimeProvider,
		Trigger: Trigger{Type: TriggerMessage, Chats: []string{}, Labels: []string{}, Keywords: []string{}},
		Limits:  defaultLimits()}
}

func defaultLimits() Limits {
	return Limits{MaxSteps: DefaultMaxSteps, MaxRecipients: DefaultMaxRecipients, MaxSends: DefaultMaxSends,
		MaxWrites: DefaultMaxWrites, MaxGroupChanges: DefaultGroupChanges, DailyRuns: DefaultDailyRuns}
}

// apply merges the input into a.
func (in Input) apply(a Agent) Agent {
	if in.Name != nil {
		a.Name = strings.TrimSpace(*in.Name)
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if in.Mode != nil {
		a.Mode = strings.TrimSpace(*in.Mode)
	}
	if in.Instructions != nil {
		a.Instructions = strings.TrimSpace(*in.Instructions)
	}
	if in.Runtime != nil {
		a.Runtime = strings.TrimSpace(*in.Runtime)
	}
	if in.Tools != nil {
		a.Tools = append([]string{}, in.Tools...)
	}
	if in.Trigger != nil {
		t := *in.Trigger
		if t.Type == "" {
			t.Type = TriggerMessage
		}
		a.Trigger = t
	}
	if in.Limits != nil {
		l := *in.Limits
		if l.MaxSteps == 0 {
			l.MaxSteps = DefaultMaxSteps
		}
		a.Limits = l.withDefaults()
	}
	return a
}

// withDefaults fills unset (zero) limits.
func (l Limits) withDefaults() Limits {
	d := defaultLimits()
	for _, p := range []struct{ v, def *int }{{&l.MaxSteps, &d.MaxSteps}, {&l.MaxRecipients, &d.MaxRecipients},
		{&l.MaxSends, &d.MaxSends}, {&l.MaxWrites, &d.MaxWrites}, {&l.MaxGroupChanges, &d.MaxGroupChanges}} {
		if *p.v == 0 {
			*p.v = *p.def
		}
	}
	return l
}

// validate checks and normalizes an agent.
func (a *Agent) validate() error {
	switch {
	case a.Name == "" || utf8.RuneCountInString(a.Name) > maxNameLen:
		return fmt.Errorf("name is required (at most %d characters)", maxNameLen)
	case a.Mode != ModeApproval && a.Mode != ModeAutonomous && a.Mode != ModeSuggest:
		return errors.New(`mode must be "aprovacao", "autonomo" or "sugerir"`)
	case a.Runtime != "" && a.Runtime != RuntimeProvider && a.Runtime != RuntimeCodexCLI:
		return errors.New(`runtime must be "provider" or "codex_cli"`)
	case utf8.RuneCountInString(a.Instructions) > maxInstructions:
		return fmt.Errorf("instructions are limited to %d characters", maxInstructions)
	case a.Trigger.Type != TriggerMessage && a.Trigger.Type != TriggerManual:
		return errors.New(`trigger.type must be "message" or "manual"`)
	case a.Limits.MaxSteps < 1 || a.Limits.MaxSteps > 20:
		return errors.New("limits.max_steps must be between 1 and 20")
	case a.Limits.MaxRecipients < 1 || a.Limits.MaxRecipients > 10:
		return errors.New("limits.max_recipients must be between 1 and 10")
	case a.Limits.MaxSends < 1 || a.Limits.MaxSends > 50:
		return errors.New("limits.max_sends must be between 1 and 50")
	case a.Limits.MaxWrites < 1 || a.Limits.MaxWrites > 100:
		return errors.New("limits.max_writes must be between 1 and 100")
	case a.Limits.MaxGroupChanges < 1 || a.Limits.MaxGroupChanges > 20:
		return errors.New("limits.max_group_changes must be between 1 and 20")
	case a.Limits.DailyRuns < 0 || a.Limits.DailyRuns > 100000:
		return errors.New("limits.daily_runs must be between 0 (no limit) and 100000")
	case len(a.Trigger.Chats) > maxFilterItems || len(a.Trigger.Labels) > maxFilterItems || len(a.Trigger.Keywords) > maxFilterItems:
		return fmt.Errorf("trigger filters accept at most %d items each", maxFilterItems)
	}
	if a.Runtime == "" {
		a.Runtime = RuntimeProvider
	}
	tools := []string{}
	for _, g := range a.Tools {
		g = strings.TrimSpace(g)
		if g == "" || slices.Contains(tools, g) {
			continue
		}
		if _, ok := groupTools[g]; !ok {
			return fmt.Errorf("unknown permission group %q (valid: %s)", g, strings.Join(Groups, ", "))
		}
		tools = append(tools, g)
	}
	a.Tools = tools
	chats := []string{}
	for _, c := range a.Trigger.Chats {
		j, err := service.ChatJID(c)
		if err != nil {
			return fmt.Errorf("trigger.chats: %q is not a phone number or JID", c)
		}
		if !slices.Contains(chats, j) {
			chats = append(chats, j)
		}
	}
	a.Trigger.Chats = chats
	a.Trigger.Labels = cleanList(a.Trigger.Labels, 100)
	a.Trigger.Keywords = cleanList(a.Trigger.Keywords, 100)
	return nil
}

func cleanList(in []string, maxLen int) []string {
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || slices.Contains(out, s) {
			continue
		}
		if utf8.RuneCountInString(s) > maxLen {
			s = string([]rune(s)[:maxLen])
		}
		out = append(out, s)
	}
	return out
}

// has reports a permission group.
func (a Agent) has(group string) bool { return slices.Contains(a.Tools, group) }

// allowedTools is the set of MCP tool names the agent may call.
func (a Agent) allowedTools() map[string]bool {
	out := map[string]bool{}
	for _, g := range a.Tools {
		for _, t := range groupTools[g] {
			out[t] = true
		}
	}
	return out
}
