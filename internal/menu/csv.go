package menu

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/store"
)

// MaxCSVBytes bounds an imported spreadsheet.
const MaxCSVBytes = 1 << 20

// CSV columns (headers are matched without accents, case or spaces).
var csvAliases = map[string]string{
	"categoria": "categoria", "category": "categoria", "secao": "categoria", "grupo": "categoria",
	"nome": "nome", "name": "nome", "produto": "nome", "item": "nome",
	"descricao": "descricao", "description": "descricao", "desc": "descricao",
	"preco": "preco", "price": "preco", "valor": "preco",
	"disponivel": "disponivel", "available": "disponivel", "ativo": "disponivel", "estoque": "disponivel",
	"imagem_url": "imagem_url", "imagem": "imagem_url", "image_url": "imagem_url", "foto": "imagem_url", "image": "imagem_url", "url_imagem": "imagem_url",
	"opcoes": "opcoes", "options": "opcoes", "opcionais": "opcoes",
	"sku": "sku", "codigo": "sku", "code": "sku", "retailer_id": "sku",
	"tags":              "tags",
	"preco_promocional": "preco_promocional", "promocao": "preco_promocional", "preco_promo": "preco_promocional",
	"sale_price": "preco_promocional", "preco_de_promocao": "preco_promocional",
	"promocao_ate": "promocao_ate", "promo_ate": "promocao_ate", "sale_until": "promocao_ate", "validade_promocao": "promocao_ate",
	"link": "link", "link_url": "link", "url": "link", "site": "link",
	"colecoes": "colecoes", "colecao": "colecoes", "collections": "colecoes", "collection": "colecoes",
	"fotos": "fotos", "imagens": "fotos", "images": "fotos", "image_urls": "fotos",
	"oculto": "oculto", "hidden": "oculto", "escondido": "oculto", "ocultar": "oculto",
}

// headerKey normalizes a header: no accents, lowercase, spaces and dashes as "_".
func headerKey(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(s, "\ufeff"))
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == ' ' || r == '-':
			b.WriteByte('_')
		default:
			b.WriteString(fold(r))
		}
	}
	return b.String()
}

// fold drops the accents of Portuguese letters.
func fold(r rune) string {
	switch r {
	case 'á', 'à', 'â', 'ã', 'ä':
		return "a"
	case 'é', 'è', 'ê', 'ë':
		return "e"
	case 'í', 'ì', 'î', 'ï':
		return "i"
	case 'ó', 'ò', 'ô', 'õ', 'ö':
		return "o"
	case 'ú', 'ù', 'û', 'ü':
		return "u"
	case 'ç':
		return "c"
	}
	return string(r)
}

// CSVResult is a parsed spreadsheet: every data row with its problems, plus problems of
// the file itself.
type CSVResult struct {
	Rows   []Row    `json:"rows"`
	Errors []string `json:"errors"`
}

// Valid returns the rows without errors.
func (r CSVResult) Valid() []Row {
	out := []Row{}
	for _, row := range r.Rows {
		if len(row.Errors) == 0 {
			out = append(out, row)
		}
	}
	return out
}

// ParseCSV reads a spreadsheet exported as CSV (comma or semicolon separated, as Excel
// in Portuguese writes it). Columns: categoria, nome, descricao, preco, disponivel,
// imagem_url and the optional opcoes, sku, tags, preco_promocional, promocao_ate, link,
// colecoes (names separated by |), fotos (photo URLs separated by |) and oculto; only
// nome is required. A cell that ExportCSV escaped against formula injection ('=...)
// is read back without the quote.
func ParseCSV(data string) CSVResult {
	res := CSVResult{Rows: []Row{}, Errors: []string{}}
	if len(data) > MaxCSVBytes {
		res.Errors = append(res.Errors, "arquivo maior que 1 MB")
		return res
	}
	if !utf8.ValidString(data) {
		res.Errors = append(res.Errors, "o arquivo n\u00e3o est\u00e1 em UTF-8: salve como CSV UTF-8 (no Excel: Salvar como > CSV UTF-8)")
		return res
	}
	data = strings.TrimPrefix(data, "\ufeff")
	if strings.TrimSpace(data) == "" {
		res.Errors = append(res.Errors, "arquivo vazio")
		return res
	}
	first, _, _ := strings.Cut(data, "\n")
	sep := ','
	if strings.Count(first, ";") > strings.Count(first, ",") {
		sep = ';'
	} else if strings.Count(first, "\t") > strings.Count(first, ",") {
		sep = '\t'
	}
	rd := csv.NewReader(strings.NewReader(data))
	rd.Comma, rd.FieldsPerRecord, rd.LazyQuotes, rd.TrimLeadingSpace = sep, -1, true, true
	header, err := rd.Read()
	if err != nil {
		res.Errors = append(res.Errors, "não foi possível ler o cabeçalho")
		return res
	}
	cols := map[string]int{}
	for i, h := range header {
		if k, ok := csvAliases[headerKey(h)]; ok {
			if _, dup := cols[k]; !dup {
				cols[k] = i
			}
		}
	}
	if _, ok := cols["nome"]; !ok {
		res.Errors = append(res.Errors, "a primeira linha precisa dos nomes das colunas, com pelo menos \"nome\" "+
			"(colunas: categoria, nome, descricao, preco, disponivel, imagem_url, opcoes)")
		return res
	}
	get := func(rec []string, k string) string {
		if i, ok := cols[k]; ok && i < len(rec) {
			return unescapeCell(strings.TrimSpace(rec[i]))
		}
		return ""
	}
	for {
		line, _ := rd.FieldPos(0)
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("linha %d: %v", line, err))
			break
		}
		empty := true
		for _, f := range rec {
			if strings.TrimSpace(f) != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		if len(res.Rows) >= store.MaxMenuItems {
			res.Errors = append(res.Errors, fmt.Sprintf("o cardápio tem no máximo %d itens: as linhas a partir da %d foram ignoradas", store.MaxMenuItems, line))
			break
		}
		line, _ = rd.FieldPos(0)
		row := Row{Line: line, Category: get(rec, "categoria"), Name: get(rec, "nome"), Description: get(rec, "descricao"),
			ImageURL: get(rec, "imagem_url"), SKU: get(rec, "sku"), Available: true}
		if t := get(rec, "tags"); t != "" {
			row.Tags = strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == '|' || r == ';' })
		}
		if p := get(rec, "preco"); p != "" {
			cents, err := ParsePrice(p)
			if err != nil {
				row.AddError("preco", PriceError("preco", p))
			}
			row.PriceCents = cents
		}
		if a, ok := ParseBool(get(rec, "disponivel")); ok {
			row.Available = a
		} else {
			row.AddError("disponivel", "disponivel deve ser sim, não ou esgotado")
		}
		if p := get(rec, "preco_promocional"); p != "" {
			cents, err := ParsePrice(p)
			if err != nil {
				row.AddError("preco_promocional", PriceError("preco_promocional", p))
			}
			row.SalePriceCents = cents
		}
		row.SaleUntil = get(rec, "promocao_ate")
		row.LinkURL = get(rec, "link")
		if c := get(rec, "colecoes"); c != "" {
			row.Collections = strings.Split(c, "|")
		}
		if f := get(rec, "fotos"); f != "" {
			row.ImageURLs = strings.FieldsFunc(f, func(r rune) bool { return r == '|' || r == ' ' || r == '\n' })
		}
		if o := get(rec, "oculto"); o != "" {
			switch h, ok := ParseBool(o); {
			case !ok:
				row.AddError("oculto", "oculto deve ser sim ou não")
			default:
				row.Hidden = h
			}
		}
		if o := get(rec, "opcoes"); o != "" {
			opts, err := ParseOptions(o)
			if err != nil {
				row.AddError("opcoes", err.Error())
			}
			row.Options = opts
		}
		NormalizeRow(&row)
		row.PriceText = FormatPrice(row.PriceCents, "BRL")
		if row.SalePriceCents > 0 {
			row.SalePriceText = FormatPrice(row.SalePriceCents, "BRL")
		}
		row.Check()
		res.Rows = append(res.Rows, row)
	}
	if len(res.Rows) == 0 && len(res.Errors) == 0 {
		res.Errors = append(res.Errors, "nenhuma linha de item depois do cabeçalho")
	}
	return res
}

// ParseBool reads a spreadsheet yes/no ("" is yes: available).
func ParseBool(s string) (bool, bool) {
	switch headerKey(s) {
	case "", "sim", "s", "yes", "y", "true", "1", "x", "disponivel", "ativo":
		return true, true
	case "nao", "n", "no", "false", "0", "esgotado", "indisponivel", "inativo":
		return false, true
	}
	return false, false
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

var (
	// "Borda (opcional)", "Adicionais (até 3)", "Molhos (mín 1, até 2)".
	groupMods = regexp.MustCompile(`\(([^)]*)\)\s*$`)
	modNumber = regexp.MustCompile(`\d+`)
	// A choice's price at its end: "G +10,00", "M + R$ 5", "Bacon 4,50", "G=+10".
	choicePrice = regexp.MustCompile(`(?i)(?:^|[\s=])([+-]?)\s*(?:r\$\s*)?(\d[\d.,]*)\s*$`)
)

// ParseOptions reads the "opcoes" cell: groups separated by ";", each "Name: choice |
// choice +price | ...". By default a group is a required single choice; "(opcional)"
// makes it optional, "(até N)" allows up to N choices and "(mín N)" requires N.
// Example: "Tamanho: P | M +5,00 | G +10,00; Borda (opcional): Catupiry +8".
func ParseOptions(s string) ([]Option, error) {
	var out []Option
	for _, g := range strings.Split(s, ";") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		name, rest, ok := strings.Cut(g, ":")
		if !ok {
			return out, fmt.Errorf("opções: %q precisa do formato \"Nome: escolha | escolha +preço\"", clipRunes(g, 40))
		}
		o := Option{Required: true, Min: 1, Max: 1}
		name = strings.TrimSpace(name)
		if m := groupMods.FindStringSubmatch(name); m != nil {
			name = strings.TrimSpace(name[:len(name)-len(m[0])])
			for _, mod := range strings.Split(m[1], ",") {
				k := headerKey(mod)
				n := 0
				if d := modNumber.FindString(k); d != "" {
					n, _ = strconv.Atoi(d)
				}
				switch {
				case strings.HasPrefix(k, "opcional"):
					o.Required, o.Min = false, 0
				case strings.HasPrefix(k, "obrigatorio"):
					o.Required, o.Min = true, max(o.Min, 1)
				case (strings.HasPrefix(k, "ate") || strings.HasPrefix(k, "max")) && n > 0:
					o.Max = n
				case strings.HasPrefix(k, "min") && n >= 0:
					o.Min, o.Required = n, n > 0
				}
			}
		}
		o.Name = name
		for _, c := range strings.Split(rest, "|") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			ch := Choice{Name: c}
			if m := choicePrice.FindStringSubmatchIndex(c); m != nil {
				sign, num := c[m[2]:m[3]], c[m[4]:m[5]]
				label := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(c[:m[0]]), "="))
				if label != "" { // "500ml" alone is a name, not a price
					cents, err := ParsePrice(num)
					if err != nil {
						return out, fmt.Errorf("opções: preço de %q não reconhecido", clipRunes(c, 40))
					}
					if sign == "-" {
						cents = -cents
					}
					ch = Choice{Name: label, PriceDeltaCents: cents}
				}
			}
			o.Choices = append(o.Choices, ch)
		}
		if o.Min > o.Max {
			o.Max = o.Min
		}
		out = append(out, o)
	}
	return out, nil
}

// ---- export ----

// ExportColumns are the columns of ExportCSV, in order (ParseCSV reads them back).
var ExportColumns = []string{"categoria", "nome", "descricao", "preco", "preco_promocional", "promocao_ate", "disponivel", "oculto",
	"sku", "link", "tags", "colecoes", "opcoes", "fotos"}

// formulaStart are the first characters a spreadsheet may read as a formula.
const formulaStart = "=+-@\t\r"

// EscapeCell protects a cell against formula injection: a value starting with = + - @
// (or a tab or carriage return) gets a leading quote, so Excel and Google Sheets show it
// as text instead of running it.
func EscapeCell(s string) string {
	if s != "" && strings.ContainsRune(formulaStart, rune(s[0])) {
		return "'" + s
	}
	return s
}

// unescapeCell undoes EscapeCell.
func unescapeCell(s string) string {
	if len(s) > 1 && s[0] == '\'' && strings.ContainsRune(formulaStart, rune(s[1])) {
		return s[1:]
	}
	return s
}

// CentsText renders cents for a spreadsheet: "1234,50" ("" for zero).
func CentsText(c int64) string {
	if c == 0 {
		return ""
	}
	neg := ""
	if c < 0 {
		neg, c = "-", -c
	}
	return neg + strconv.FormatInt(c/100, 10) + "," + fmt.Sprintf("%02d", c%100)
}

// OptionsCell renders option groups in the "opcoes" format ParseOptions reads:
// "Tamanho: P | M +5,00; Borda (opcional): Catupiry +8,00".
func OptionsCell(opts []store.MenuOption) string {
	groups := make([]string, 0, len(opts))
	for _, o := range opts {
		var mods []string
		if !o.Required {
			mods = append(mods, "opcional")
		} else if o.Min > 1 {
			mods = append(mods, "mín "+itoa(o.Min))
		}
		if o.Max > 1 {
			mods = append(mods, "até "+itoa(o.Max))
		}
		name := o.Name
		if len(mods) > 0 {
			name += " (" + strings.Join(mods, ", ") + ")"
		}
		choices := make([]string, 0, len(o.Choices))
		for _, c := range o.Choices {
			ch := c.Name
			switch {
			case c.PriceDeltaCents > 0:
				ch += " +" + CentsText(c.PriceDeltaCents)
			case c.PriceDeltaCents < 0:
				ch += " " + CentsText(c.PriceDeltaCents)
			case ch != "" && ch[len(ch)-1] >= '0' && ch[len(ch)-1] <= '9':
				ch += " +0" // "Lata 350" would read back as "Lata" +R$ 350,00
			}
			choices = append(choices, ch)
		}
		groups = append(groups, name+": "+strings.Join(choices, " | "))
	}
	return strings.Join(groups, "; ")
}

// ExportCSV renders the menu as a spreadsheet (semicolon separated, UTF-8 with BOM, as
// Excel in Portuguese opens it) that ParseCSV imports back. photoURL gives a photo's
// address ("" leaves it out: an upload has none). Every cell is escaped against formula
// injection.
func ExportCSV(cats []store.MenuCategory, items []store.MenuItem, colls []store.MenuCollection, photoURL func(store.MenuImage) string) []byte {
	catName := map[string]string{}
	for _, c := range cats {
		catName[c.ID] = c.Name
	}
	collName := map[string]string{}
	for _, c := range colls {
		collName[c.ID] = c.Name
	}
	var b strings.Builder
	b.WriteString("\ufeff")
	w := csv.NewWriter(&b)
	w.Comma = ';'
	_ = w.Write(ExportColumns)
	yes := func(v bool) string {
		if v {
			return "sim"
		}
		return "não"
	}
	for _, it := range items {
		var cs, photos []string
		for _, id := range it.CollectionIDs {
			if n := collName[id]; n != "" {
				cs = append(cs, n)
			}
		}
		for _, im := range it.Images {
			if u := photoURL(im); u != "" {
				photos = append(photos, u)
			}
		}
		rec := []string{catName[it.CategoryID], it.Name, it.Description, CentsText(it.PriceCents), CentsText(it.SalePriceCents), it.SaleUntil,
			yes(it.Available), yes(it.Hidden), it.SKU, it.LinkURL, strings.Join(it.Tags, ", "), strings.Join(cs, "|"), OptionsCell(it.Options),
			strings.Join(photos, "|")}
		for i := range rec {
			rec[i] = EscapeCell(rec[i])
		}
		_ = w.Write(rec)
	}
	w.Flush()
	return []byte(b.String())
}
