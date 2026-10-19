package main

import (
	"context"
	"errors"
	"time"

	"fyne.io/systray"
	"github.com/douglasdf99/litezap/internal/desktop"
)

// notifyingConnector tells the tray menu when the dashboard changed a connection.
type notifyingConnector struct {
	*desktop.Connector
	changed chan struct{}
}

func (n notifyingConnector) Connect(ctx context.Context, id string) (desktop.AIResult, error) {
	defer n.poke()
	return n.Connector.Connect(ctx, id)
}

func (n notifyingConnector) Disconnect(ctx context.Context, id string) (desktop.AIResult, error) {
	defer n.poke()
	return n.Connector.Disconnect(ctx, id)
}

func (n notifyingConnector) poke() {
	select {
	case n.changed <- struct{}{}:
	default:
	}
}

type aiAction struct {
	id      string
	connect bool
}

// aiMenu runs the "Conectar a IA" submenu: one checkbox item per client (checked when
// connected; a click connects, or repairs a stale entry) and a "Desconectar" item shown
// while an entry exists.
func (a *app) aiMenu(parent *systray.MenuItem) {
	type row struct {
		client      desktop.AIClient
		item, disc  *systray.MenuItem
		last        desktop.AIStatus
		initialized bool
	}
	acts := make(chan aiAction)
	var rows []*row
	for _, cl := range desktop.AIClients() {
		r := &row{client: cl,
			item: parent.AddSubMenuItemCheckbox(cl.Name, "Conectar o LiteZap ao "+cl.Name, false),
			disc: parent.AddSubMenuItem("Desconectar do "+cl.Name, "Remove o LiteZap da configuração do "+cl.Name)}
		r.disc.Hide()
		rows = append(rows, r)
		go func() {
			for {
				select {
				case <-r.item.ClickedCh:
					acts <- aiAction{cl.ID, true}
				case <-r.disc.ClickedCh:
					acts <- aiAction{cl.ID, false}
				}
			}
		}()
	}
	refresh := func() {
		for _, r := range rows {
			st := a.ai.Status(r.client.ID)
			if r.initialized && st == r.last {
				continue
			}
			r.last, r.initialized = st, true
			title, tip := r.client.Name, "Conectar o LiteZap ao "+r.client.Name
			switch {
			case st.Error != "":
				title, tip = r.client.Name+" (configuração inválida)", st.Error
			case st.Connected:
				title, tip = r.client.Name+" (conectado)", "Conectado: "+st.Path
			case st.Present:
				title, tip = r.client.Name+" (atualizar)", "A entrada do LiteZap aponta para outro programa: clique para corrigir"
			case !st.Installed:
				title = r.client.Name + " (não encontrado)"
			}
			r.item.SetTitle(title)
			r.item.SetTooltip(tip)
			if st.Connected {
				r.item.Check()
			} else {
				r.item.Uncheck()
			}
			if st.Present {
				r.disc.Show()
			} else {
				r.disc.Hide()
			}
		}
	}
	refresh()
	// The files can change behind our back (the user edits them, Claude Code rewrites
	// ~/.claude.json): poll now and then.
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case act := <-acts:
			a.runAI(act)
			refresh()
		case <-a.aiChanged:
			refresh()
		case <-tick.C:
			refresh()
		}
	}
}

func (a *app) runAI(act aiAction) {
	name := desktop.ClientName(act.id)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var msg string
	if act.connect {
		res, err := a.ai.Connect(ctx, act.id)
		switch {
		case errors.Is(err, desktop.ErrExeNotInstalled):
			msg = "Mova o LiteZap para a pasta Aplicativos, abra-o de lá e conecte de novo."
		case err != nil:
			msg = "Não foi possível conectar ao " + name + ":\n\n" + err.Error()
		case !res.Changed:
			msg = "O LiteZap já está conectado ao " + name + ". " + desktop.RestartHint(act.id)
		default:
			msg = "Pronto! O LiteZap foi conectado ao " + name + ".\n\n" + desktop.RestartHint(act.id)
		}
		a.log.Info("connect AI client", "client", act.id, "path", res.Path, "backup", res.Backup, "cli", res.CLI, "err", err)
	} else {
		res, err := a.ai.Disconnect(ctx, act.id)
		if err != nil {
			msg = "Não foi possível desconectar do " + name + ":\n\n" + err.Error()
		} else {
			msg = "O LiteZap foi desconectado do " + name + "."
		}
		a.log.Info("disconnect AI client", "client", act.id, "path", res.Path, "cli", res.CLI, "err", err)
	}
	if err := desktop.Alert("LiteZap", msg); err != nil {
		a.log.Warn("alert failed", "err", err, "msg", msg)
	}
}
