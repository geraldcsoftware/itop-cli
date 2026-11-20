package itop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/geraldcsoftware/itop-cli/internal/config"
)

func newTestClient(t *testing.T, srv *httptest.Server, auth string, timeout time.Duration) *Client {
	t.Helper()
	c, err := New(config.Config{URL: srv.URL, Auth: auth, Timeout: timeout}, "jdoe", "s3cr3t", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSecretOnlyInBodyAndRedacted(t *testing.T) {
	var form url.Values
	var rawQuery string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form, rawQuery = r.PostForm, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"code":100,"message":"bad login s3cr3t for jdoe"}`))
	}))
	defer srv.Close()

	keys := func() string {
		var k []string
		for key := range form {
			k = append(k, key)
		}
		sort.Strings(k)
		return strings.Join(k, ",")
	}

	for _, tc := range []struct{ auth, wantKeys string }{
		{"password", "auth_pwd,auth_user,json_data,version"},
		{"token", "auth_token,json_data,version"},
	} {
		err := newTestClient(t, srv, tc.auth, time.Second).CheckCredentials(context.Background())
		if got := keys(); got != tc.wantKeys {
			t.Errorf("%s mode form keys = %s, want %s", tc.auth, got, tc.wantKeys)
		}
		if rawQuery != "" {
			t.Errorf("%s mode sent a query string: %q", tc.auth, rawQuery)
		}
		if tc.auth == "token" && form.Get("auth_token") != "s3cr3t" {
			t.Error("token mode did not send the secret as auth_token")
		}
		if err == nil || strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "jdoe") ||
			!strings.Contains(err.Error(), "[REDACTED]") {
			t.Errorf("%s mode error not redacted: %v", tc.auth, err)
		}
	}
}

func TestClassifyErrors(t *testing.T) {
	var mu sync.Mutex
	var reply string
	var delay time.Duration
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body, wait := reply, delay
		mu.Unlock()
		time.Sleep(wait)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "password", 50*time.Millisecond)

	for _, tc := range []struct {
		name  string
		reply string
		delay time.Duration
		want  ErrorKind
	}{
		{"deadline", `{"code":0}`, 300 * time.Millisecond, ErrUnreachable},
		{"login rejected", `{"code":1,"message":"Invalid login"}`, 0, ErrAuth},
		{"unknown class", `{"code":100,"message":"Error: Unknown class 'Foo'"}`, 0, ErrSchema},
		{"unknown attribute", `{"code":100,"message":"Unknown attribute 'bar' for class 'Change'"}`, 0, ErrSchema},
		{"other", `{"code":100,"message":"OQL syntax error"}`, 0, ErrAPI},
	} {
		mu.Lock()
		reply, delay = tc.reply, tc.delay
		mu.Unlock()
		var e *Error
		if err := c.CheckCredentials(context.Background()); !errors.As(err, &e) || e.Kind != tc.want {
			t.Errorf("%s: got %v, want kind %d", tc.name, err, tc.want)
		}
	}

	closed := httptest.NewTLSServer(http.NotFoundHandler())
	cc := newTestClient(t, closed, "password", time.Second)
	closed.Close()
	var e *Error
	if err := cc.CheckCredentials(context.Background()); !errors.As(err, &e) || e.Kind != ErrUnreachable {
		t.Errorf("closed listener: got %v, want ErrUnreachable", err)
	}
}

func TestRedirectNeverFollowed(t *testing.T) {
	hit := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	err := newTestClient(t, srv, "password", time.Second).CheckCredentials(context.Background())
	var e *Error
	if hit || !errors.As(err, &e) || e.Kind != ErrAPI {
		t.Errorf("redirect followed or not reported: hit=%v err=%v", hit, err)
	}
}

func TestWritesAreFixedPayloads(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		var req map[string]any
		_ = json.Unmarshal([]byte(r.PostForm.Get("json_data")), &req)
		op, _ := req["operation"].(string)
		key, _ := req["key"].(string)
		mu.Lock()
		if op != "core/get" {
			sent = append(sent, req)
		}
		mu.Unlock()
		obj := func(class, id string) string {
			return `{"code":0,"objects":{"` + class + `::` + id + `":{"class":"` + class + `","key":"` + id + `","fields":{"status":"new"}}}}`
		}
		switch {
		case op == "core/create":
			_, _ = w.Write([]byte(obj("WorkOrder", "99")))
		case op == "core/update":
			_, _ = w.Write([]byte(`{"code":0,"objects":{}}`))
		case strings.HasPrefix(key, "SELECT Incident"):
			_, _ = w.Write([]byte(obj("Incident", "7")))
		case strings.HasPrefix(key, "SELECT Change"):
			_, _ = w.Write([]byte(obj("NormalChange", "41")))
		case strings.HasPrefix(key, "SELECT WorkOrder"):
			_, _ = w.Write([]byte(obj("WorkOrder", "5")))
		case strings.HasPrefix(key, "SELECT Ticket"):
			_, _ = w.Write([]byte(obj("NormalChange", "41")))
		default:
			_, _ = w.Write([]byte(`{"code":0,"objects":null}`))
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "password", time.Second)
	ctx := context.Background()
	must := func(kind TargetKind, ref string) Target {
		tg, err := c.ResolveTarget(ctx, kind, ref)
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}
	inc, chg, wo := must(TargetIncident, "I-000007"), must(TargetChange, "C-000041"), must(TargetWorkOrder, "5")
	last := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		if len(sent) != 1 {
			t.Fatalf("want exactly 1 write, got %d", len(sent))
		}
		r := sent[0]
		sent = nil
		return r
	}
	check := func(name string, got, want map[string]any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s payload = %v, want %v", name, got, want)
		}
	}

	if err := c.SetDescription(ctx, chg, "<p>x</p>"); err != nil {
		t.Fatal(err)
	}
	check("description", last(), map[string]any{"operation": "core/update", "comment": "itop-cli", "class": "NormalChange",
		"key": "41", "output_fields": "id", "fields": map[string]any{"description": "<p>x</p>"}})

	if err := c.SetDescription(ctx, wo, "<p>w</p>"); err != nil {
		t.Fatal(err)
	}
	check("wo description", last(), map[string]any{"operation": "core/update", "comment": "itop-cli", "class": "WorkOrder",
		"key": "5", "output_fields": "id", "fields": map[string]any{"description": "<p>w</p>"}})

	if err := c.AppendPrivateLog(ctx, inc, "<p>n</p>"); err != nil {
		t.Fatal(err)
	}
	check("log", last(), map[string]any{"operation": "core/update", "comment": "itop-cli", "class": "Incident",
		"key": "7", "output_fields": "id",
		"fields": map[string]any{"private_log": map[string]any{"add_item": map[string]any{"message": "<p>n</p>", "format": "html"}}}})

	id, err := c.CreateWorkOrder(ctx, chg, WorkOrderInput{Name: "Step", DescriptionHTML: "<p>s</p>", TeamID: "3", AgentID: "4",
		StartDate: "2026-11-21 08:00:00"})
	if err != nil || id != "99" {
		t.Fatalf("create: id=%q err=%v", id, err)
	}
	check("create", last(), map[string]any{"operation": "core/create", "comment": "itop-cli", "class": "WorkOrder",
		"output_fields": "id", "fields": map[string]any{"ticket_id": "41", "name": "Step", "description": "<p>s</p>",
			"team_id": "3", "agent_id": "4", "start_date": "2026-11-21 08:00:00"}})

	var refused *RefusedError
	if err := c.SetDescription(ctx, inc, "x"); !errors.As(err, &refused) {
		t.Errorf("description on Incident: %v", err)
	}
	if err := c.AppendPrivateLog(ctx, wo, "x"); !errors.As(err, &refused) {
		t.Errorf("log on WorkOrder: %v", err)
	}
	if _, err := c.CreateWorkOrder(ctx, inc, WorkOrderInput{Name: "n"}); !errors.As(err, &refused) {
		t.Errorf("work order on Incident: %v", err)
	}
	var zero Target
	if err := c.SetDescription(ctx, zero, "x"); !errors.As(err, &refused) {
		t.Errorf("description on zero Target: %v", err)
	}
	if err := c.AppendPrivateLog(ctx, zero, "x"); !errors.As(err, &refused) {
		t.Errorf("log on zero Target: %v", err)
	}
	if _, err := c.CreateWorkOrder(ctx, zero, WorkOrderInput{Name: "n"}); !errors.As(err, &refused) {
		t.Errorf("work order on zero Target: %v", err)
	}
	for _, op := range []string{"core/delete", "core/apply_stimulus"} {
		if _, err := c.post(ctx, op, nil); err == nil {
			t.Errorf("%s was not rejected", op)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 0 {
		t.Errorf("refused calls sent %d requests", len(sent))
	}
}
