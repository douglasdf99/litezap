package service

import (
	"context"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/menu"
)

// Every invalid row of a preview has its reason in the top-level errors ("linha N: ..."),
// with its line and field in error_details, and a per-row error.
func TestMenuImportErrorsListEveryInvalidRow(t *testing.T) {
	e := newMsgEnv(t)
	m := newMenu(e)
	ctx := context.Background()
	v, err := m.ImportRows(ctx, "i1", MenuRowsInput{Rows: []menu.Row{
		{Name: "Calabresa", Price: "45,90", Available: true},
		{Name: "Coca", Price: "12", Available: true},
		{Name: "Suco", Price: "abc", Available: true},
		{Name: "", Price: "1", Available: true},
	}, MenuImportOptions: MenuImportOptions{DryRun: true}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Invalid != 2 || len(v.Errors) != 2 || len(v.ErrorDetails) != 2 || len(v.FileErrors) != 0 {
		t.Fatalf("view = %+v", v)
	}
	if !strings.HasPrefix(v.Errors[0], "linha 3: preço inválido 'abc'") || v.ErrorDetails[0].Field != "preco" || v.ErrorDetails[0].Line != 3 {
		t.Fatalf("price error = %q %+v", v.Errors[0], v.ErrorDetails[0])
	}
	if !strings.HasPrefix(v.Errors[1], "linha 4: nome é obrigatório") || v.ErrorDetails[1].Field != "nome" {
		t.Fatalf("name error = %q %+v", v.Errors[1], v.ErrorDetails[1])
	}
	if !strings.Contains(v.Rows[2].Error, "preço inválido 'abc'") || v.Rows[0].Error != "" {
		t.Fatalf("row errors = %q / %q", v.Rows[2].Error, v.Rows[0].Error)
	}

	// The spreadsheet import reports its rows the same way (CSV line numbers).
	v, err = m.ImportCSV(ctx, "i1", MenuCSVInput{CSV: "nome;preco;disponivel\nPizza;45,90;sim\nCoca;doze;talvez\n", MenuImportOptions: MenuImportOptions{DryRun: true}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Invalid != 1 || len(v.Errors) != 2 || !strings.HasPrefix(v.Errors[0], "linha 3: preço inválido 'doze'") ||
		v.ErrorDetails[1].Field != "disponivel" {
		t.Fatalf("csv view = %+v", v)
	}
	// A problem of the file itself is a file error (and is listed in errors too).
	v, _ = m.ImportCSV(ctx, "i1", MenuCSVInput{CSV: "categoria;preco\nx;1\n", MenuImportOptions: MenuImportOptions{DryRun: true}})
	if len(v.FileErrors) != 1 || len(v.Errors) != 1 || len(v.ErrorDetails) != 0 {
		t.Fatalf("file error view = %+v", v)
	}
}
