package attendant

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

// Modes (presets). "custom" starts empty.
const (
	ModeSeller  = "vendedor"
	ModeOrder   = "pedido"
	ModeSDR     = "sdr"
	ModeSupport = "suporte"
	ModeCustom  = "custom"
)

// Actions an agent may be allowed to take. handoff is always available (it only makes
// the agent safer); the others emit events for webhooks/n8n.
const (
	ActionHandoff     = "handoff"
	ActionLead        = "lead"
	ActionAppointment = "appointment"
	ActionOrderDraft  = "order_draft"
)

var knownActions = []string{ActionHandoff, ActionLead, ActionAppointment, ActionOrderDraft}

// Voice reply modes.
const (
	VoiceOff       = "off"
	VoiceWhenAudio = "when_audio" // answer a voice note with a voice note
	VoiceAlways    = "always"
)

// Knowledge entry kinds.
var knowledgeKinds = []string{"faq", "product", "hours", "policy", "info"}

// KnowledgeEntry is one item of the business knowledge base.
type KnowledgeEntry struct {
	Kind    string   `json:"kind"`            // faq | product | hours | policy | info
	Title   string   `json:"title"`           // question, product name or label
	Content string   `json:"content"`         // answer or description
	Price   *float64 `json:"price,omitempty"` // products
}

// Profile is an agent's "training": mode, script, tone, business context.
type Profile struct {
	Name            string           `json:"name"`
	Mode            string           `json:"mode"`
	Objective       string           `json:"objective"`
	Steps           []string         `json:"steps"`
	Tone            string           `json:"tone"`
	HandoffCriteria []string         `json:"handoff_criteria"`
	Actions         []string         `json:"actions"`
	Business        string           `json:"business"`
	Knowledge       []KnowledgeEntry `json:"knowledge"`
	Language        string           `json:"language"`
	Currency        string           `json:"currency"`
	// VoiceReply: off (default) | when_audio | always (needs a speech model).
	VoiceReply string `json:"voice_reply"`
	// CallFollowUp writes to a caller after an automatic call rejection.
	CallFollowUp bool `json:"call_followup"`
	// CallHint guides the call follow-up message (optional).
	CallHint string `json:"call_hint"`
	// MaxReplyChars caps a reply (default 1000, at most 4000).
	MaxReplyChars int `json:"max_reply_chars"`
	// HideContactName keeps the contact's WhatsApp name away from the model.
	HideContactName bool `json:"hide_contact_name"`
}

// Limits.
const (
	maxNameLen      = 80
	maxObjective    = 2000
	maxSteps        = 30
	maxStepLen      = 500
	maxToneLen      = 500
	maxCriteria     = 20
	maxCriterionLen = 300
	maxBusinessLen  = 20000
	maxKnowledge    = 300
	maxTitleLen     = 200
	maxContentLen   = 4000
	maxKnowledgeLen = 120000 // all entries together
	maxCallHintLen  = 500
	defaultReplyMax = 1000
	hardReplyMax    = 4000
)

// Presets returns the editable starting point of each mode.
func Presets() map[string]Profile {
	return map[string]Profile{
		ModeSeller: {Mode: ModeSeller,
			Objective: "Ajudar o cliente a escolher produtos do catálogo e conduzir para a compra, sem pressão.",
			Steps: []string{
				"Cumprimente e pergunte o que o cliente procura.",
				"Faça no máximo duas perguntas para entender a necessidade (uso, orçamento, preferências).",
				"Recomende de 1 a 3 produtos do catálogo com preço e o motivo da indicação.",
				"Responda objeções com fatos do catálogo e das políticas (troca, entrega, pagamento).",
				"Convide a fechar a compra: explique o próximo passo (link, pagamento, retirada ou entrega).",
			},
			Tone:            "Simpático, consultivo e direto; mensagens curtas de WhatsApp.",
			HandoffCriteria: []string{"O cliente pede desconto fora da política ou condição especial.", "O cliente quer falar com uma pessoa.", "Reclamação ou cliente irritado."},
			Actions:         []string{ActionHandoff, ActionLead},
		},
		ModeOrder: {Mode: ModeOrder,
			Objective: "Montar o pedido do cliente com itens do catálogo, confirmar quantidades, endereço e forma de entrega, e pedir confirmação.",
			Steps: []string{
				"Pergunte o que o cliente deseja pedir.",
				"Para cada item, confirme o produto do catálogo e a quantidade; nunca invente produtos ou preços.",
				"Pergunte o endereço de entrega (ou se é retirada) e observações.",
				"Envie o resumo do pedido: itens, quantidades, preço unitário, total e endereço.",
				"Peça para o cliente confirmar o pedido com um \"sim\" antes de considerar fechado.",
			},
			Tone:            "Cordial e organizado; liste os itens de forma clara.",
			HandoffCriteria: []string{"Item fora do catálogo ou pedido personalizado.", "Problema com pedido anterior, pagamento ou entrega.", "O cliente quer falar com uma pessoa."},
			Actions:         []string{ActionHandoff, ActionOrderDraft},
		},
		ModeSDR: {Mode: ModeSDR,
			Objective: "Qualificar o contato e coletar nome, necessidade e melhor horário para uma consulta ou reunião.",
			Steps: []string{
				"Cumprimente e pergunte como pode ajudar.",
				"Descubra a necessidade principal em uma ou duas perguntas.",
				"Pergunte o nome do cliente, se ainda não souber.",
				"Pergunte o dia e o horário de preferência, dentro do horário de atendimento.",
				"Confirme os dados e diga que a equipe vai confirmar o agendamento.",
			},
			Tone:            "Profissional e acolhedor.",
			HandoffCriteria: []string{"Urgência ou emergência.", "Pergunta técnica que a base de conhecimento não responde.", "O cliente quer falar com uma pessoa."},
			Actions:         []string{ActionHandoff, ActionLead, ActionAppointment},
		},
		ModeSupport: {Mode: ModeSupport,
			Objective: "Resolver dúvidas usando apenas a base de conhecimento e as políticas da empresa.",
			Steps: []string{
				"Entenda o problema; peça um detalhe por vez se faltar informação.",
				"Responda com base na base de conhecimento, em passos curtos.",
				"Confirme se resolveu.",
				"Se não houver resposta na base, diga que vai chamar a equipe e passe para um humano.",
			},
			Tone:            "Paciente, empático e claro.",
			HandoffCriteria: []string{"A base de conhecimento não cobre o assunto.", "Pedido de reembolso, cancelamento ou dados pessoais.", "Cliente irritado ou que pede uma pessoa."},
			Actions:         []string{ActionHandoff},
		},
		ModeCustom: {Mode: ModeCustom, Actions: []string{ActionHandoff}},
	}
}

// withPreset fills the empty script fields of p from its mode's preset.
func withPreset(p Profile) Profile {
	pre, ok := Presets()[p.Mode]
	if !ok {
		return p
	}
	if p.Objective == "" {
		p.Objective = pre.Objective
	}
	if p.Steps == nil {
		p.Steps = pre.Steps
	}
	if p.Tone == "" {
		p.Tone = pre.Tone
	}
	if p.HandoffCriteria == nil {
		p.HandoffCriteria = pre.HandoffCriteria
	}
	if p.Actions == nil {
		p.Actions = pre.Actions
	}
	return p
}

// normalize trims fields and applies defaults; Validate checks the result.
func (p *Profile) normalize() {
	p.Name = strings.TrimSpace(p.Name)
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = ModeCustom
	}
	p.Objective = strings.TrimSpace(p.Objective)
	p.Tone = strings.TrimSpace(p.Tone)
	p.Business = strings.TrimSpace(p.Business)
	p.CallHint = strings.TrimSpace(p.CallHint)
	p.Steps = cleanList(p.Steps)
	p.HandoffCriteria = cleanList(p.HandoffCriteria)
	acts := []string{}
	for _, a := range cleanList(p.Actions) {
		a = strings.ToLower(a)
		if !slices.Contains(acts, a) {
			acts = append(acts, a)
		}
	}
	p.Actions = acts
	if p.Knowledge == nil {
		p.Knowledge = []KnowledgeEntry{}
	}
	for i := range p.Knowledge {
		k := &p.Knowledge[i]
		k.Kind = strings.ToLower(strings.TrimSpace(k.Kind))
		if k.Kind == "" {
			k.Kind = "info"
		}
		k.Title, k.Content = strings.TrimSpace(k.Title), strings.TrimSpace(k.Content)
	}
	if p.Language = strings.TrimSpace(p.Language); p.Language == "" {
		p.Language = "pt-BR"
	}
	if p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency)); p.Currency == "" {
		p.Currency = "BRL"
	}
	if p.VoiceReply = strings.TrimSpace(p.VoiceReply); p.VoiceReply == "" {
		p.VoiceReply = VoiceOff
	}
	if p.MaxReplyChars == 0 {
		p.MaxReplyChars = defaultReplyMax
	}
}

func cleanList(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func tooLong(s string, n int) bool { return utf8.RuneCountInString(s) > n }

// Validate checks a normalized profile.
func (p *Profile) Validate() error {
	switch {
	case p.Name == "" || tooLong(p.Name, maxNameLen):
		return fmt.Errorf("name is required (at most %d characters)", maxNameLen)
	case !slices.Contains([]string{ModeSeller, ModeOrder, ModeSDR, ModeSupport, ModeCustom}, p.Mode):
		return errors.New("mode must be vendedor, pedido, sdr, suporte or custom")
	case tooLong(p.Objective, maxObjective):
		return fmt.Errorf("objective is limited to %d characters", maxObjective)
	case len(p.Steps) > maxSteps:
		return fmt.Errorf("at most %d steps", maxSteps)
	case tooLong(p.Tone, maxToneLen):
		return fmt.Errorf("tone is limited to %d characters", maxToneLen)
	case len(p.HandoffCriteria) > maxCriteria:
		return fmt.Errorf("at most %d handoff criteria", maxCriteria)
	case tooLong(p.Business, maxBusinessLen):
		return fmt.Errorf("business is limited to %d characters", maxBusinessLen)
	case len(p.Knowledge) > maxKnowledge:
		return fmt.Errorf("at most %d knowledge entries", maxKnowledge)
	case tooLong(p.CallHint, maxCallHintLen):
		return fmt.Errorf("call_hint is limited to %d characters", maxCallHintLen)
	case !slices.Contains([]string{VoiceOff, VoiceWhenAudio, VoiceAlways}, p.VoiceReply):
		return errors.New("voice_reply must be off, when_audio or always")
	case p.MaxReplyChars < 50 || p.MaxReplyChars > hardReplyMax:
		return fmt.Errorf("max_reply_chars must be between 50 and %d", hardReplyMax)
	case tooLong(p.Language, 20) || len(p.Currency) != 3:
		return errors.New("language must be a short tag (pt-BR) and currency a 3-letter code (BRL)")
	}
	for _, s := range p.Steps {
		if tooLong(s, maxStepLen) {
			return fmt.Errorf("each step is limited to %d characters", maxStepLen)
		}
	}
	for _, s := range p.HandoffCriteria {
		if tooLong(s, maxCriterionLen) {
			return fmt.Errorf("each handoff criterion is limited to %d characters", maxCriterionLen)
		}
	}
	for _, a := range p.Actions {
		if !slices.Contains(knownActions, a) {
			return fmt.Errorf("unknown action %q (handoff, lead, appointment, order_draft)", a)
		}
	}
	total := 0
	for _, k := range p.Knowledge {
		switch {
		case !slices.Contains(knowledgeKinds, k.Kind):
			return fmt.Errorf("knowledge kind %q must be faq, product, hours, policy or info", k.Kind)
		case k.Title == "" && k.Content == "":
			return errors.New("a knowledge entry needs a title or a content")
		case tooLong(k.Title, maxTitleLen) || tooLong(k.Content, maxContentLen):
			return fmt.Errorf("knowledge title is limited to %d and content to %d characters", maxTitleLen, maxContentLen)
		case k.Price != nil && (*k.Price < 0 || *k.Price > 1e9 || math.IsNaN(*k.Price)):
			return errors.New("knowledge price must be between 0 and 1e9")
		}
		total += len(k.Title) + len(k.Content)
	}
	if total > maxKnowledgeLen {
		return fmt.Errorf("the knowledge base is limited to %d bytes", maxKnowledgeLen)
	}
	return nil
}

func (p Profile) allows(action string) bool {
	return action == ActionHandoff || slices.Contains(p.Actions, action)
}
