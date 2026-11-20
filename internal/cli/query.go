package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/geraldcsoftware/itop-cli/internal/cache"
	"github.com/geraldcsoftware/itop-cli/internal/itop"
	"github.com/geraldcsoftware/itop-cli/internal/render"
)

// selectClass captures the class a SELECT reads from: the first identifier
// after SELECT, which stays the class when an alias or a join follows.
var selectClass = regexp.MustCompile(`(?i)^\s*SELECT\s+([A-Za-z_][A-Za-z0-9_]*)\b`)

// classPattern is the shape of an iTop class name; it also keeps a class used
// as a cache file name from escaping the cache directory.
var classPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ticketClasses get a ticket-shaped default column set; every other class
// falls back to friendlyname.
var ticketClasses = map[string]bool{
	"Ticket": true, "Incident": true, "UserRequest": true, "Problem": true,
	"Change": true, "NormalChange": true, "RoutineChange": true,
	"EmergencyChange": true, "ApprovedChange": true,
}

func defaultFields(class string) []string {
	if ticketClasses[class] {
		return []string{"ref", "title", "status", "org_name"}
	}
	return []string{"friendlyname"}
}

func splitFields(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// blobSummary turns an iTop blob value (a map holding data, mimetype and
// filename) into a one-line summary so base64 never reaches the output.
func blobSummary(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	data, hasData := m["data"].(string)
	if !hasData {
		return "", false
	}
	if _, hasType := m["mimetype"]; !hasType {
		return "", false
	}
	name, _ := m["filename"].(string)
	mime, _ := m["mimetype"].(string)
	if data == "" && name == "" && mime == "" {
		return "", true
	}
	size := base64.RawStdEncoding.DecodedLen(len(strings.TrimRight(data, "=")))
	return fmt.Sprintf("<blob: %s, %s, %d bytes>", name, mime, size), true
}

// cellText renders one attribute value as a string. Blobs are summarised;
// other structured values are written as compact JSON.
func cellText(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := blobSummary(v); ok {
		return s
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return fmt.Sprint(t)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// objectRows lays objects out as class, id and then the attribute columns:
// the requested fields in the order given, or every attribute returned (sorted)
// when the request was "*".
func objectRows(objs []itop.Object, fields []string) render.Rows {
	cols := fields
	if len(fields) == 1 && fields[0] == "*" {
		seen := map[string]bool{}
		cols = nil
		for _, o := range objs {
			for k := range o.Fields {
				if !seen[k] {
					seen[k] = true
					cols = append(cols, k)
				}
			}
		}
		sort.Strings(cols)
	}
	rows := render.Rows{Columns: append([]string{"class", "id"}, cols...)}
	for _, o := range objs {
		row := []string{o.Class, o.ID}
		for _, c := range cols {
			row = append(row, cellText(o.Fields[c]))
		}
		rows.Rows = append(rows.Rows, row)
	}
	return rows
}

func runQuery(args []string, globals globalFlags, stdout, stderr io.Writer) int {
	var g globalFlags
	var fieldList string
	fs := newFlagSet("query", stderr)
	addGlobals(fs, &g, globals)
	fs.StringVar(&fieldList, "fields", "", "attribute codes to print")
	fs.StringVar(&fieldList, "f", "", "shorthand for --fields")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return exitParse(err, stdout)
	}
	if err := g.validate(); err != nil {
		return fail(stderr, g.output, err)
	}
	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		return fail(stderr, g.output, errors.New(`query takes one OQL SELECT, quoted, for example itop query "SELECT Server WHERE name LIKE '%web%'"`))
	}
	oql := positional[0]
	m := selectClass.FindStringSubmatch(oql)
	if m == nil {
		return fail(stderr, g.output, errors.New("query accepts only an OQL SELECT"))
	}
	class := m[1]
	fields := splitFields(fieldList)
	if len(fields) == 0 {
		fields = defaultFields(class)
	}

	c, err := newSession(g)
	if err != nil {
		return fail(stderr, g.output, err)
	}
	objs, truncated, err := c.GetAll(context.Background(), class, oql, fields, g.limit)
	if err != nil {
		return fail(stderr, g.output, err)
	}
	if code := renderRows(objectRows(objs, fields), g, stdout, stderr); code != ExitOK {
		return code
	}
	if truncated {
		fmt.Fprintf(stderr, "itop: showing the first %d objects; more match. Narrow the query or raise --limit (at most %d).\n", g.limit, maxLimit)
	}
	return ExitOK
}

// fieldsCachePath is the cache file for a class, or "" when there is no cache
// directory.
func fieldsCachePath(class string) string {
	root := cache.Root()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "fields", class+".json")
}

func readFieldsCache(class string) ([]string, bool) {
	path := fieldsCachePath(class)
	if path == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var codes []string
	if json.Unmarshal(b, &codes) != nil || len(codes) == 0 {
		return nil, false
	}
	return codes, true
}

func writeFieldsCache(class string, codes []string) error {
	path := fieldsCachePath(class)
	if path == "" {
		return errors.New("no cache directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(codes)
	if err != nil {
		return err
	}
	// A private temporary file renamed into place replaces the target (or a
	// symlink standing at its name) instead of writing through it.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fields-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// runFields lists a class's attribute codes. iTop's REST API has no schema
// operation, so the codes come from the keys of one object fetched with every
// attribute, and are cached per class.
func runFields(args []string, globals globalFlags, stdout, stderr io.Writer) int {
	var g globalFlags
	var refresh bool
	fs := newFlagSet("fields", stderr)
	addGlobals(fs, &g, globals)
	fs.BoolVar(&refresh, "refresh", false, "ignore the cached list and ask iTop again")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return exitParse(err, stdout)
	}
	if err := g.validate(); err != nil {
		return fail(stderr, g.output, err)
	}
	if len(positional) != 1 || !classPattern.MatchString(positional[0]) {
		return fail(stderr, g.output, errors.New("fields takes one class name, for example itop fields Incident"))
	}
	class := positional[0]

	codes, cached := []string(nil), false
	if !refresh {
		codes, cached = readFieldsCache(class)
	}
	if !cached {
		c, err := newSession(g)
		if err != nil {
			return fail(stderr, g.output, err)
		}
		objs, err := c.Get(context.Background(), class, "SELECT "+class, nil, 1, 0)
		if err != nil {
			return fail(stderr, g.output, err)
		}
		if len(objs) == 0 {
			return fail(stderr, g.output, &itop.Error{Kind: itop.ErrAPI,
				Msg: fmt.Sprintf("no %s object exists, so its attribute codes cannot be read", class)})
		}
		for k := range objs[0].Fields {
			codes = append(codes, k)
		}
		sort.Strings(codes)
		if err := writeFieldsCache(class, codes); err != nil {
			fmt.Fprintf(stderr, "itop: warning: could not cache the field list: %v\n", err)
		}
	}
	rows := render.Rows{Columns: []string{"attribute"}}
	for _, code := range codes {
		rows.Rows = append(rows.Rows, []string{code})
	}
	return renderRows(rows, g, stdout, stderr)
}
