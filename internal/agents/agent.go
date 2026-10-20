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
	GroupReply      = "responder_no_chat"
	GroupOtherChats = "enviar_outros_chats"
	GroupLabels     = "etiquetas"
	GroupGroups     = "grupos"
	GroupMedia      = "midia"
)

// Groups lists the permission groups in display order.
var Groups = []string{GroupRead, GroupReply, GroupOtherChats, GroupLabels, GroupGroups, GroupMedia}

// RiskyGroups need an explicit confirmation in the dashboard.
var RiskyGroups = []string{GroupOtherChats, GroupGroups}

// groupTools maps each group to the MCP tools it grants. enviar_outros_chats also lifts
// the trigger-chat restriction of every send tool the agent has.
var groupTools = map[string][]string{
	GroupRead: {"get_instance_status", "list_chats", "get_messages", "get_message_status", "list_contacts", "get_contact",
		"list_groups", "get_group", "list_labels"},
	GroupReply:      {"send_message", "send_reaction"},
	GroupOtherChats: {"send_message", "send_reaction"},
	GroupLabels:     {"label_chat"},
	GroupGroups:     {"create_group", "update_group", "manage_group_participants", "get_group_invite_link"},
	GroupMedia:      {"send_media"},
}

// writeTools change something outside LiteZap: in aprovacao they become drafts, in
// sugerir notes, in the simulator dry runs.
var writeTools = map[string]bool{"send_message": true, "send_reaction": true, "send_media": true, "label_chat": true,
	"create_group": true, "update_group": true, "manage_group_participants": true}

// sendTools send a message to their "to" (restricted to the trigger chat unless
// enviar_outros_chats).
var sendTools = map[string]bool{"send_message": true, "send_reaction": true, "send_media": true}

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
	// DailyRuns is the maximum runs per UTC day (default 200; 0 = no agent-level limit, the
	// provider caps still apply).
	DailyRuns int `json:"daily_runs"`
}

// Defaults.
const (
	DefaultMaxSteps      = 8
	DefaultMaxRecipients = 3
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
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
}

// defaultAgent is the starting point of a new agent: read + reply in the trigger chat,
// approval mode, new 1:1 messages, off until enabled.
func defaultAgent() Agent {
	return Agent{Mode: ModeApproval, Tools: []string{GroupRead, GroupReply},
		Trigger: Trigger{Type: TriggerMessage, Chats: []string{}, Labels: []string{}, Keywords: []string{}},
		Limits:  Limits{MaxSteps: DefaultMaxSteps, MaxRecipients: DefaultMaxRecipients, DailyRuns: DefaultDailyRuns}}
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
		if l.MaxRecipients == 0 {
			l.MaxRecipients = DefaultMaxRecipients
		}
		a.Limits = l
	}
	return a
}

// validate checks and normalizes an agent.
func (a *Agent) validate() error {
	switch {
	case a.Name == "" || utf8.RuneCountInString(a.Name) > maxNameLen:
		return fmt.Errorf("name is required (at most %d characters)", maxNameLen)
	case a.Mode != ModeApproval && a.Mode != ModeAutonomous && a.Mode != ModeSuggest:
		return errors.New(`mode must be "aprovacao", "autonomo" or "sugerir"`)
	case utf8.RuneCountInString(a.Instructions) > maxInstructions:
		return fmt.Errorf("instructions are limited to %d characters", maxInstructions)
	case a.Trigger.Type != TriggerMessage && a.Trigger.Type != TriggerManual:
		return errors.New(`trigger.type must be "message" or "manual"`)
	case a.Limits.MaxSteps < 1 || a.Limits.MaxSteps > 20:
		return errors.New("limits.max_steps must be between 1 and 20")
	case a.Limits.MaxRecipients < 1 || a.Limits.MaxRecipients > 10:
		return errors.New("limits.max_recipients must be between 1 and 10")
	case a.Limits.DailyRuns < 0 || a.Limits.DailyRuns > 100000:
		return errors.New("limits.daily_runs must be between 0 (no limit) and 100000")
	case len(a.Trigger.Chats) > maxFilterItems || len(a.Trigger.Labels) > maxFilterItems || len(a.Trigger.Keywords) > maxFilterItems:
		return fmt.Errorf("trigger filters accept at most %d items each", maxFilterItems)
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
