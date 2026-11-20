// Package cli is the command dispatcher for the itop binary.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/geraldcsoftware/itop-cli/internal/config"
	"github.com/geraldcsoftware/itop-cli/internal/credential"
	"github.com/geraldcsoftware/itop-cli/internal/itop"
	"github.com/geraldcsoftware/itop-cli/internal/render"
)

// BuildInfo identifies the running build; the linker sets it through main.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

var build = BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}

// SetBuildInfo records the build identity reported by `itop version`.
func SetBuildInfo(b BuildInfo) { build = b }

const (
	defaultLimit = 50
	maxLimit     = 1000
	// defaultMaxColWidth caps a table cell so one long value cannot push the
	// other columns off the screen.
	defaultMaxColWidth = 50
)

const usage = `itop reads an iTop ITSM instance over its REST API.

Usage:
  itop [global flags] <command> [flags] [args]

Commands:
  ping                              check the credentials and print the user and host
  query (q) "<OQL>" [-f a,b]        run an OQL SELECT and print the objects
  fields <Class>                    list the attribute codes of a class
  version                           print the build version

Problems, configuration items and any other class are read with ` + "`itop query`" + `,
for example ` + "`itop query \"SELECT Problem WHERE status != 'closed'\"`" + ` or
` + "`itop query \"SELECT Server WHERE name LIKE '%web%'\" -f name,status,managementip`" + `.
Use ` + "`itop fields <Class>`" + ` to find the attribute codes a class accepts.

Global flags (before or after the command):
  --config (-c) <path>   config file (default: $ITOP_CONFIG or ~/.config/itop-cli/config.toml)
  --output (-o) <fmt>    json|table|text|auto (default auto, or $ITOP_OUTPUT): table on a
                         terminal, tab-separated text otherwise
  --limit <n>            maximum rows for query (default 50, at most 1000)
  --help (-h)            show this help

query flags:
  --fields (-f) <list>   comma-separated attribute codes to print

Blob values (such as Attachment contents) are never printed; they appear as
<blob: name, type, N bytes>.

Exit codes:
  0  success
  1  config, usage or credential error, including iTop rejecting the login
  2  iTop unreachable or timed out
  3  unknown class or field
  4  other iTop error (OQL syntax, permission denied)
  5  refused by the CLI's own guard
  6  ref or id not found
`

// commandAliases maps each shorthand to the command it stands for. It is an
// explicit map rather than a prefix match so a new command can never silently
// take over an established shorthand.
var commandAliases = map[string]string{
	"i":       "incident",
	"request": "user-request",
	"r":       "user-request",
	"cr":      "change-request",
	"wo":      "work-order",
	"q":       "query",
}

// canonicalCommand resolves an alias to the full command name and leaves
// anything else untouched, so an unknown command is reported as typed.
func canonicalCommand(name string) string {
	if full, ok := commandAliases[name]; ok {
		return full
	}
	return name
}

// newHTTPClient supplies the HTTP client for a session. Nil lets itop.New
// build its own; tests swap in the TLS test server's client.
var newHTTPClient = func() *http.Client { return nil }

// globalFlags are the flags every command accepts.
type globalFlags struct {
	config string
	output string
	limit  int
}

func envDefaults() globalFlags {
	output := os.Getenv("ITOP_OUTPUT")
	if output == "" {
		output = render.AutoFormat
	}
	return globalFlags{output: output, limit: defaultLimit}
}

// addGlobals registers the global flags, each long name and shorthand bound to
// one variable. def supplies the defaults, so a value given before the command
// is inherited and the same flag after the command wins.
func addGlobals(fs *flag.FlagSet, g *globalFlags, def globalFlags) {
	fs.StringVar(&g.config, "config", def.config, "config file path")
	fs.StringVar(&g.config, "c", def.config, "shorthand for --config")
	fs.StringVar(&g.output, "output", def.output, "output format")
	fs.StringVar(&g.output, "o", def.output, "shorthand for --output")
	fs.IntVar(&g.limit, "limit", def.limit, "maximum rows")
}

func (g globalFlags) validate() error {
	if err := render.Valid(g.output); err != nil {
		return err
	}
	if g.limit < 1 || g.limit > maxLimit {
		return fmt.Errorf("--limit must be between 1 and %d", maxLimit)
	}
	return nil
}

// newFlagSet builds a FlagSet whose parse errors go to stderr; -h is mapped to
// the full usage by exitParse.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	return fs
}

func exitParse(err error, stdout io.Writer) int {
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return ExitOK
	}
	return ExitUsage
}

// parseInterspersed parses fs allowing flags before or after the positional
// arguments, which Go's flag package otherwise stops at.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// Run executes the command line and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	globals := envDefaults()
	fs := newFlagSet("itop", stderr)
	addGlobals(fs, &globals, globals)
	if err := fs.Parse(args); err != nil {
		return exitParse(err, stdout)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	cmdArgs := rest[1:]
	switch canonicalCommand(rest[0]) {
	case "ping":
		return runPing(cmdArgs, globals, stdout, stderr)
	case "query":
		return runQuery(cmdArgs, globals, stdout, stderr)
	case "fields":
		return runFields(cmdArgs, globals, stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "itop %s (%s, %s)\n", build.Version, build.Commit, build.Date)
		return ExitOK
	case "help":
		fmt.Fprint(stdout, usage)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", rest[0], usage)
		return ExitUsage
	}
}

// fail reports err on stderr honouring the output format and returns its exit
// code.
func fail(stderr io.Writer, output string, err error) int {
	render.Error(stderr, output, err.Error())
	return exitFor(err)
}

// isTerminal reports whether w is a character device. It is a variable so a
// test can exercise the terminal branch without a real one.
var isTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// resolveOutput maps render.AutoFormat onto table for a terminal and text
// otherwise; any explicit format passes through.
func resolveOutput(format string, w io.Writer) string {
	if format != render.AutoFormat {
		return format
	}
	if isTerminal(w) {
		return "table"
	}
	return "text"
}

// renderRows writes rows in the requested format.
func renderRows(rows render.Rows, g globalFlags, stdout, stderr io.Writer) int {
	output := resolveOutput(g.output, stdout)
	if err := render.Render(stdout, output, rows, render.Options{MaxColWidth: defaultMaxColWidth}); err != nil {
		render.Error(stderr, output, err.Error())
		return ExitUsage
	}
	return ExitOK
}

// openSession loads the config, resolves the username and credential, and
// builds the client. The secret stays in process memory.
func openSession(g globalFlags) (*itop.Client, config.Config, error) {
	path := g.config
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, config.Config{}, err
	}
	var opts credential.Options
	if cfg.BWSAccessToken != "" {
		tok, err := credential.ResolveScalar(cfg.BWSAccessToken)
		if err != nil {
			return nil, config.Config{}, fmt.Errorf("resolving bws.accessToken: %w", err)
		}
		opts.BWSAccessToken = tok
	}
	cred, err := credential.ResolveWith(cfg.Credential, opts)
	if err != nil {
		return nil, config.Config{}, fmt.Errorf("resolving credential: %w", err)
	}
	if cred.Password == "" {
		return nil, config.Config{}, errors.New("the credential resolved to an empty secret")
	}
	var clientOpts []itop.Option
	if hc := newHTTPClient(); hc != nil {
		clientOpts = append(clientOpts, itop.WithHTTPClient(hc))
	}
	c, err := itop.New(cfg, cfg.Username, cred.Password, clientOpts...)
	if err != nil {
		return nil, config.Config{}, err
	}
	return c, cfg, nil
}

// newSession is openSession for commands that need only the client.
func newSession(g globalFlags) (*itop.Client, error) {
	c, _, err := openSession(g)
	return c, err
}

// runPing checks the login and prints the username and URL host, never the
// secret.
func runPing(args []string, globals globalFlags, stdout, stderr io.Writer) int {
	var g globalFlags
	fs := newFlagSet("ping", stderr)
	addGlobals(fs, &g, globals)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return exitParse(err, stdout)
	}
	if len(positional) > 0 {
		return fail(stderr, g.output, errors.New("ping takes no arguments"))
	}
	if err := g.validate(); err != nil {
		return fail(stderr, g.output, err)
	}
	c, cfg, err := openSession(g)
	if err != nil {
		return fail(stderr, g.output, err)
	}
	if err := c.CheckCredentials(context.Background()); err != nil {
		return fail(stderr, g.output, err)
	}
	host := cfg.URL
	if u, err := url.Parse(cfg.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	return renderRows(render.Rows{
		Columns: []string{"user", "host"},
		Rows:    [][]string{{cfg.Username, host}},
	}, g, stdout, stderr)
}
