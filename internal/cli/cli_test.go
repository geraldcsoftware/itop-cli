package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geraldcsoftware/itop-cli/internal/itop"
)

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, ExitOK},
		{"plain config or credential error", errors.New("config: url is required"), ExitUsage},
		{"login rejected", &itop.Error{Kind: itop.ErrAuth}, ExitUsage},
		{"unreachable", &itop.Error{Kind: itop.ErrUnreachable}, ExitUnreachable},
		{"unknown class or field", &itop.Error{Kind: itop.ErrSchema}, ExitSchema},
		{"other iTop error", &itop.Error{Kind: itop.ErrAPI}, ExitAPI},
		{"refused", &itop.RefusedError{Ref: "I-000123", ActualClass: "UserRequest", Wanted: "Incident"}, ExitRefused},
		{"not found", &itop.NotFoundError{What: "incident"}, ExitNotFound},
		{"wrapped kind", fmt.Errorf("fields: %w", &itop.Error{Kind: itop.ErrSchema}), ExitSchema},
	}
	for _, tc := range cases {
		if got := exitFor(tc.err); got != tc.want {
			t.Errorf("%s: exitFor = %d, want %d", tc.name, got, tc.want)
		}
	}
	if want := []int{0, 1, 2, 3, 4, 5, 6}; fmt.Sprint([]int{ExitOK, ExitUsage, ExitUnreachable, ExitSchema, ExitAPI, ExitRefused, ExitNotFound}) != fmt.Sprint(want) {
		t.Error("exit constants do not match the published table")
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"query"}, &out, &errOut); code != ExitUsage {
		t.Errorf("query without OQL exited %d, want %d", code, ExitUsage)
	}
}

func TestQueryNeverPrintsBlobs(t *testing.T) {
	const b64 = "YWJj" // "abc"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"message":"","objects":{"Attachment::7":{"code":0,"message":"","class":"Attachment","key":"7","fields":{"item_name":"I-000123","contents":{"data":"` + b64 + `","mimetype":"text/plain","filename":"a.log"}}}}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf("url = %q\nusername = \"jdoe\"\ncredential = \"env:ITOP_CLI_TEST_PW\"\n", srv.URL)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ITOP_CLI_TEST_PW", "s3cr3t")
	t.Setenv("XDG_CACHE_HOME", dir)
	old := newHTTPClient
	newHTTPClient = srv.Client
	defer func() { newHTTPClient = old }()

	for _, format := range []string{"text", "table", "json"} {
		var out, errOut bytes.Buffer
		code := Run([]string{"-c", cfg, "-o", format, "query", "SELECT Attachment", "-f", "item_name,contents"}, &out, &errOut)
		if code != ExitOK {
			t.Fatalf("%s: exit %d: %s", format, code, errOut.String())
		}
		if !strings.Contains(out.String(), "<blob: a.log, text/plain, 3 bytes>") {
			t.Errorf("%s: no blob summary in %q", format, out.String())
		}
		if strings.Contains(out.String(), b64) {
			t.Errorf("%s: base64 leaked into output %q", format, out.String())
		}
	}
}
