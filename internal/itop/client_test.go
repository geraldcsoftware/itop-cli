package itop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
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
	var reply string
	var delay time.Duration
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		_, _ = w.Write([]byte(reply))
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
		reply, delay = tc.reply, tc.delay
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
