package main

import (
	"context"
	"os"
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
	id string
	// kind: "connect" (config file), "disconnect" or "extension" (Claude Desktop .mcpb).
	kind string
}

// extensionMessage is what to tell the user after handing the .mcpb to Claude Desktop.
func extensionMessage(res desktop.ExtensionResult, configEntry bool) string {
	msg := "O Claude Desktop vai mostrar a instalação da extensão LiteZap: clique em Instalar.\n\n" +
		"Se nada aparecer, abra o Claude Desktop, vá em Configurações > Extensões e arraste para lá o arquivo:\n" + res.Path
	if configEntry {
		msg += "\n\nVocê também tem o LiteZap no arquivo de configuração do Claude: depois de instalar a extensão, " +
			"use \"Desconectar do Claude Desktop\" para não ver as ferramentas em dobro."
	}
	return msg
}

// aiMenu runs the "Conectar a IA" submenu: for Claude Desktop, "Instalar extensão
// (recomendado)" when the app ships the .mcpb; one checkbox item per client (checked when
// connected; a click connects, or repairs a stale entry) and a "Desconectar" item shown
// while an entry exists.
func (a *app) aiMenu(parent *systray.MenuItem) {
	type row struct {
		client      desktop.AIClient
		item, disc  *systray.MenuItem
		last        desktop.AIStatus
		sig         fileSig
		initialized bool
	}
	acts := make(chan aiAction)
	extension := a.ai.ExtensionAvailable()
	var rows []*row
	for _, cl := range desktop.AIClients() {
		if cl.ID == desktop.ClaudeDesktop && extension {
			ext := parent.AddSubMenuItem("Claude Desktop: instalar extensão (recomendado)",
				"Abre a extensão LiteZap no Claude Desktop, que mostra a instalação")
			go func() {
				for range ext.ClickedCh {
					acts <- aiAction{cl.ID, "extension"}
				}
			}()
		}
		r := &row{client: cl,
			item: parent.AddSubMenuItemCheckbox(cl.Name, "Conectar o LiteZap ao "+cl.Name, false),
			disc: parent.AddSubMenuItem("Desconectar do "+cl.Name, "Remove o LiteZap da configuração do "+cl.Name)}
		r.disc.Hide()
		rows = append(rows, r)
		go func() {
			for {
				select {
				case <-r.item.ClickedCh:
					acts <- aiAction{cl.ID, "connect"}
				case <-r.disc.ClickedCh:
					acts <- aiAction{cl.ID, "disconnect"}
				}
			}
		}()
	}
	// refresh re-reads a client's config only when its size or mtime changed (or force):
	// ~/.claude.json can be large.
	refresh := func(force bool) {
		for _, r := range rows {
			p, _ := a.ai.ConfigPath(r.client.ID)
			sig := statSig(p)
			if r.initialized && !force && sig == r.sig {
				continue
			}
			r.sig = sig
			st := a.ai.Status(r.client.ID)
			if r.initialized && st == r.last {
				continue
			}
			r.last, r.initialized = st, true
			name := r.client.Name
			if r.client.ID == desktop.ClaudeDesktop && extension {
				name = "Claude Desktop: configurar manualmente (arquivo de config)"
			}
			title, tip := name, "Conectar o LiteZap ao "+r.client.Name
			switch {
			case st.Error != "":
				title, tip = name+" (configuração inválida)", st.Error
			case st.Connected && st.RestartNeeded:
				title, tip = name+" (conectado, reinicie o Claude)", st.Hint
			case st.Connected:
				title, tip = name+" (conectado)", "Conectado: "+st.Path
			case st.Present:
				title, tip = name+" (atualizar)", "A entrada do LiteZap aponta para outro programa: clique para corrigir"
			case !st.Installed:
				title = name + " (não encontrado)"
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
	refresh(true)
	// The files can change behind our back (the user edits them, Claude Code rewrites
	// ~/.claude.json): check now and then.
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case act := <-acts:
			a.runAI(act)
			refresh(true)
		case <-a.aiChanged:
			refresh(true)
		case <-tick.C:
			refresh(false)
		}
	}
}

type fileSig struct {
	exists  bool
	size    int64
	modTime time.Time
}

func statSig(path string) fileSig {
	fi, err := os.Stat(path)
	if err != nil {
		return fileSig{}
	}
	return fileSig{true, fi.Size(), fi.ModTime()}
}

func (a *app) runAI(act aiAction) {
	name := desktop.ClientName(act.id)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var msg string
	switch act.kind {
	case "extension":
		res, err := a.ai.InstallExtension()
		if err != nil {
			msg = "Não foi possível abrir a extensão no Claude Desktop:\n\n" + err.Error()
		} else {
			msg = extensionMessage(res, a.ai.Status(act.id).Present)
		}
		a.log.Info("install Claude Desktop extension", "path", res.Path, "running", res.Running, "err", err)
	case "connect":
		res, err := a.ai.Connect(ctx, act.id)
		switch {
		case err != nil:
			msg = "Não foi possível conectar ao " + name + ":\n\n" + err.Error()
		case !res.Changed:
			msg = "O LiteZap já está conectado ao " + name + ".\n\n" + res.Hint
		default:
			msg = "Pronto! O LiteZap foi conectado ao " + name + ".\n\n" + res.Hint
		}
		if err == nil && res.Running && act.id == desktop.ClaudeDesktop {
			msg += "\n\nSe o Claude apagar a configuração ao fechar, o LiteZap a refaz sozinho."
		}
		a.log.Info("connect AI client", "client", act.id, "path", res.Path, "backup", res.Backup, "cli", res.CLI, "running", res.Running, "err", err)
	default:
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
