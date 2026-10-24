package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/tabwriter"
)

// Format is an --output value.
type Format string

const (
	Table    Format = "table"
	JSON     Format = "json"
	NDJSON   Format = "ndjson"
	CSV      Format = "csv"
	Markdown Format = "markdown"
)

// Formats lists every format in help order.
var Formats = []Format{Table, JSON, NDJSON, CSV, Markdown}

// ParseFormat validates an --output value.
func ParseFormat(s string) (Format, error) {
	for _, f := range Formats {
		if strings.EqualFold(s, string(f)) {
			return f, nil
		}
	}
	return "", Usagef("unknown output format %q (want table, json, ndjson, csv or markdown)", s)
}

// Rows is the tabular view of a result, used by table, csv and markdown.
type Rows struct {
	Headers []string
	Rows    [][]string
	// Footer lines are printed after a table, or after markdown, never in csv.
	Footer []string
}

// Options controls rendering.
type Options struct {
	Format Format
	Color  bool
}

// Render writes data (json, ndjson) or rows (table, csv, markdown).
// For ndjson, data should be a slice; each element becomes one line.
func Render(w io.Writer, opts Options, data any, rows Rows) error {
	switch opts.Format {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	case NDJSON:
		return renderNDJSON(w, data)
	case CSV:
		cw := csv.NewWriter(w)
		if err := cw.Write(rows.Headers); err != nil {
			return err
		}
		if err := cw.WriteAll(rows.Rows); err != nil {
			return err
		}
		return cw.Error()
	case Markdown:
		return renderMarkdown(w, rows)
	default:
		return renderTable(w, rows, opts.Color)
	}
}

func renderNDJSON(w io.Writer, data any) error {
	enc := json.NewEncoder(w)
	v := reflect.ValueOf(data)
	if v.Kind() != reflect.Slice {
		return enc.Encode(data)
	}
	for i := 0; i < v.Len(); i++ {
		if err := enc.Encode(v.Index(i).Interface()); err != nil {
			return err
		}
	}
	return nil
}

const (
	ansiBold  = "\x1b[1m"
	ansiReset = "\x1b[0m"
)

func renderTable(w io.Writer, rows Rows, color bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(rows.Headers) > 0 {
		h := strings.Join(rows.Headers, "\t")
		if color {
			// Wrap each cell, so tabwriter still measures equal widths.
			h = ansiBold + strings.Join(rows.Headers, ansiReset+"\t"+ansiBold) + ansiReset
		}
		fmt.Fprintln(tw, h)
	}
	for _, r := range rows.Rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return footer(w, rows.Footer)
}

func renderMarkdown(w io.Writer, rows Rows) error {
	esc := func(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
	line := func(cells []string) {
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = esc(c)
		}
		fmt.Fprintf(w, "| %s |\n", strings.Join(parts, " | "))
	}
	if len(rows.Headers) > 0 {
		line(rows.Headers)
		sep := make([]string, len(rows.Headers))
		for i := range sep {
			sep[i] = "---"
		}
		fmt.Fprintf(w, "| %s |\n", strings.Join(sep, " | "))
	}
	for _, r := range rows.Rows {
		line(r)
	}
	return footer(w, rows.Footer)
}

func footer(w io.Writer, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return err
		}
	}
	return nil
}
