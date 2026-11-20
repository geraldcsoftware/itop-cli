// Package render writes a rectangular result as text, table or JSON. The
// format is chosen at the one pivot, Render.
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
)

// Rows is a result set; every row has one cell per column.
type Rows struct {
	Columns []string
	Rows    [][]string
}

// Options carries cross-format choices.
type Options struct {
	// MaxColWidth caps a table cell at this many display cells, truncating
	// with an ellipsis; 0 means unlimited. Text and JSON carry values whole.
	MaxColWidth int
}

// AutoFormat resolves to table on a terminal and text otherwise. The CLI
// resolves it before Render, so every renderer is a pure function of its
// inputs.
const AutoFormat = "auto"

const ellipsis = "…"

type renderer func(w io.Writer, rows Rows, opts Options) error

var renderers = map[string]renderer{
	"text":  renderText,
	"json":  renderJSON,
	"table": renderTable,
}

// Formats returns every accepted --output value: the concrete formats sorted,
// then AutoFormat.
func Formats() []string {
	names := make([]string, 0, len(renderers)+1)
	for name := range renderers {
		names = append(names, name)
	}
	sort.Strings(names)
	return append(names, AutoFormat)
}

// Valid reports whether format is a concrete format or AutoFormat.
func Valid(format string) error {
	if format == AutoFormat {
		return nil
	}
	if _, ok := renderers[format]; !ok {
		return fmt.Errorf("unknown output format %q (supported: %s)", format, strings.Join(Formats(), ", "))
	}
	return nil
}

// Render writes rows to w in the named concrete format.
func Render(w io.Writer, format string, rows Rows, opts Options) error {
	r, ok := renderers[format]
	if !ok {
		return fmt.Errorf("unknown output format %q (supported: %s)", format, strings.Join(Formats(), ", "))
	}
	return r(w, rows, opts)
}

// renderText prints a tab-separated header and rows. Control characters in a
// value, including escape sequences, become spaces so stored data cannot drive
// a terminal or split a row; JSON output keeps values whole.
func renderText(w io.Writer, rows Rows, _ Options) error {
	if len(rows.Columns) == 0 {
		return nil
	}
	cells := func(row []string) string {
		out := make([]string, len(rows.Columns))
		for i := range out {
			out[i] = sanitize(cell(row, i))
		}
		return strings.Join(out, "\t")
	}
	if _, err := fmt.Fprintln(w, cells(rows.Columns)); err != nil {
		return err
	}
	for _, row := range rows.Rows {
		if _, err := fmt.Fprintln(w, cells(row)); err != nil {
			return err
		}
	}
	return nil
}

// renderJSON prints an array of objects keyed by column name, keys in column
// order. Every value is a string.
func renderJSON(w io.Writer, rows Rows, _ Options) error {
	var b strings.Builder
	b.WriteString("[")
	for ri, row := range rows.Rows {
		if ri > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n  {")
		for ci, col := range rows.Columns {
			if ci > 0 {
				b.WriteString(", ")
			}
			key, err := marshal(col)
			if err != nil {
				return err
			}
			val, err := marshal(cell(row, ci))
			if err != nil {
				return err
			}
			b.Write(key)
			b.WriteString(": ")
			b.Write(val)
		}
		b.WriteString("}")
	}
	if len(rows.Rows) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// renderTable prints an aligned ASCII table with a row-count footer.
func renderTable(w io.Writer, rows Rows, opts Options) error {
	if len(rows.Columns) == 0 {
		return nil
	}
	t := table.NewWriter()
	t.SetStyle(table.StyleDefault)
	// go-pretty upper-cases headers by default; an attribute code is
	// case-sensitive and must survive verbatim.
	t.Style().Format.Header = text.FormatDefault
	t.Style().Options.SeparateRows = false
	if opts.MaxColWidth > 0 {
		cfgs := make([]table.ColumnConfig, len(rows.Columns))
		for i := range rows.Columns {
			cfgs[i] = table.ColumnConfig{
				Number:   i + 1,
				WidthMax: opts.MaxColWidth,
				// Truncate rather than wrap so one long cell cannot balloon
				// the table's height.
				WidthMaxEnforcer: func(col string, maxLen int) string { return text.Snip(col, maxLen, ellipsis) },
			}
		}
		t.SetColumnConfigs(cfgs)
	}
	header := make(table.Row, len(rows.Columns))
	for i, c := range rows.Columns {
		header[i] = sanitize(c)
	}
	t.AppendHeader(header)
	for _, row := range rows.Rows {
		out := make(table.Row, len(rows.Columns))
		for i := range rows.Columns {
			out[i] = sanitize(cell(row, i))
		}
		t.AppendRow(out)
	}
	if _, err := fmt.Fprintln(w, t.Render()); err != nil {
		return err
	}
	noun := "rows"
	if len(rows.Rows) == 1 {
		noun = "row"
	}
	_, err := fmt.Fprintf(w, "(%d %s)\n", len(rows.Rows), noun)
	return err
}

// marshal encodes v as JSON without escaping <, > and &, so HTML that iTop
// stores reads as written. The trailing newline the encoder adds is removed.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func cell(row []string, i int) string {
	if i < len(row) {
		return row[i]
	}
	return ""
}

// sanitize collapses control characters (C0, DEL and C1, so ESC and tab and
// newline too) to spaces, so a value cannot break row framing or inject
// terminal escape sequences. JSON output keeps the value whole.
func sanitize(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// Error writes a failure. In JSON mode it is a {"error": ...} document so
// `-o json | jq` never breaks on the error path.
func Error(w io.Writer, format, msg string) {
	if format == "json" {
		doc, _ := marshal(map[string]string{"error": msg})
		fmt.Fprintln(w, string(doc))
		return
	}
	fmt.Fprintln(w, "itop: "+sanitize(msg))
}
