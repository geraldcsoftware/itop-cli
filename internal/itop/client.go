// Package itop is a read-oriented client for the iTop REST/JSON API. The
// operations it can send are fixed by an allowlist, and every error text it
// returns has the secret and the username redacted.
package itop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/geraldcsoftware/itop-cli/internal/config"
)

const (
	// maxResponseBytes caps the body read for any single request.
	maxResponseBytes = 50 << 20
	// pageSize is the per-request page size GetAll asks iTop for.
	pageSize = 100
	redacted = "[REDACTED]"
)

// allowedOps is the only set of REST operations post will send.
var allowedOps = map[string]bool{
	"core/check_credentials": true,
	"list_operations":        true,
	"core/get":               true,
	"core/update":            true,
	"core/create":            true,
}

var errTooLarge = errors.New("response too large")

// ErrorKind classifies a failure so the caller can choose a recovery step.
type ErrorKind int

const (
	// ErrUnreachable is a transport failure or a deadline.
	ErrUnreachable ErrorKind = iota
	// ErrAuth is iTop rejecting the login.
	ErrAuth
	// ErrSchema is an unknown class or attribute.
	ErrSchema
	// ErrAPI is any other iTop or protocol error.
	ErrAPI
)

// Error is a classified failure. Msg is already redacted.
type Error struct {
	Kind ErrorKind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Object is one iTop object. Class is the concrete class from the response
// (for example NormalChange), which may be a subclass of the class queried.
type Object struct {
	Class  string
	ID     string
	Fields map[string]any
}

// BlobMeta describes a streamed blob attribute.
type BlobMeta struct {
	Filename string
	MimeType string
	Size     int64
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// Client talks to one iTop instance.
type Client struct {
	endpoint   string
	apiVersion string
	auth       string
	user       string
	secret     string
	timeout    time.Duration
	http       *http.Client
}

type response struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Objects orderedObjects `json:"objects"`
}

type rawObject struct {
	Class  string         `json:"class"`
	Key    flexString     `json:"key"`
	Fields map[string]any `json:"fields"`
}

// orderedObjects keeps the order iTop returned objects in; a Go map would
// lose the OQL ordering.
type orderedObjects []rawObject

func (o *orderedObjects) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if ok, err := openObject(dec); err != nil || !ok {
		return err
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			return err
		}
		var ro rawObject
		if err := dec.Decode(&ro); err != nil {
			return err
		}
		*o = append(*o, ro)
	}
	return nil
}

// flexString accepts a JSON string or number.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case string:
		*f = flexString(t)
	case float64:
		*f = flexString(strconv.FormatFloat(t, 'f', -1, 64))
	}
	return nil
}

// New builds a Client. The URL must be https. user and secret are the
// resolved credentials; in token mode secret is the personal token.
func New(cfg config.Config, user, secret string, opts ...Option) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, &Error{Kind: ErrAPI, Msg: "itop url must be an https:// URL"}
	}
	version := cfg.APIVersion
	if version == "" {
		version = "1.3"
	}
	c := &Client{
		endpoint:   normalizeEndpoint(cfg.URL),
		apiVersion: version,
		auth:       cfg.Auth,
		user:       user,
		secret:     secret,
		timeout:    cfg.Timeout,
	}
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: cfg.Timeout}
	}
	// The credentials travel in the POST body, which Go re-sends on a 307 or
	// 308 redirect. Redirects are refused on a copy so a caller's client is
	// left untouched; the non-200 check then reports the redirect as an error.
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &hc
	return c, nil
}

// normalizeEndpoint ensures the URL ends in /webservices/rest.php.
func normalizeEndpoint(raw string) string {
	t := strings.TrimRight(raw, "/")
	switch {
	case strings.Contains(t, "/webservices/rest.php"):
		return t
	case strings.HasSuffix(t, "/webservices"):
		return t + "/rest.php"
	default:
		return t + "/webservices/rest.php"
	}
}

func (c *Client) redact(s string) string {
	for _, v := range []string{c.secret, c.user} {
		if v != "" {
			s = strings.ReplaceAll(s, v, redacted)
		}
	}
	return s
}

func (c *Client) fail(kind ErrorKind, format string, a ...any) *Error {
	return &Error{Kind: kind, Msg: c.redact(fmt.Sprintf(format, a...))}
}

// classify maps an iTop failure code and message to an error kind.
func (c *Client) classify(code int, message string) *Error {
	kind := ErrAPI
	lower := strings.ToLower(message)
	switch {
	case code == 1:
		kind = ErrAuth
	case strings.Contains(lower, "unknown class"), strings.Contains(lower, "unknown attribute"):
		kind = ErrSchema
	}
	return c.fail(kind, "iTop error (code %d): %s", code, message)
}

// do sends one allowlisted operation and returns the open response. The
// caller must call cancel once the body is consumed.
func (c *Client) do(ctx context.Context, op string, payload map[string]any) (*http.Response, context.CancelFunc, error) {
	if !allowedOps[op] {
		return nil, nil, &Error{Kind: ErrAPI, Msg: "operation not permitted: " + op}
	}
	body := map[string]any{"operation": op}
	for k, v := range payload {
		body[k] = v
	}
	js, err := json.Marshal(body)
	if err != nil {
		return nil, nil, c.fail(ErrAPI, "encoding request: %v", err)
	}
	form := url.Values{"version": {c.apiVersion}, "json_data": {string(js)}}
	if c.auth == "token" {
		form.Set("auth_token", c.secret)
	} else {
		form.Set("auth_user", c.user)
		form.Set("auth_pwd", c.secret)
	}

	cancel := context.CancelFunc(func() {})
	if c.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		cancel()
		return nil, nil, c.fail(ErrAPI, "building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, nil, c.fail(ErrUnreachable, "iTop unreachable: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, nil, c.fail(ErrAPI, "iTop returned HTTP %d", resp.StatusCode)
	}
	return resp, cancel, nil
}

// post sends an allowlisted operation and returns the decoded response. A
// non-zero iTop code is returned as a classified error.
func (c *Client) post(ctx context.Context, op string, payload map[string]any) (*response, error) {
	resp, cancel, err := c.do(ctx, op, payload)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer resp.Body.Close()

	dec := json.NewDecoder(&capReader{r: resp.Body, n: maxResponseBytes})
	dec.UseNumber()
	var out response
	if err := dec.Decode(&out); err != nil {
		return nil, c.readError(err)
	}
	if out.Code != 0 {
		return nil, c.classify(out.Code, out.Message)
	}
	return &out, nil
}

func (c *Client) readError(err error) *Error {
	switch {
	case errors.Is(err, errTooLarge):
		return c.fail(ErrAPI, "response exceeds %d MB", maxResponseBytes>>20)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return c.fail(ErrUnreachable, "iTop unreachable: %v", err)
	}
	return c.fail(ErrAPI, "malformed iTop response: %v", err)
}

// capReader fails with errTooLarge once more than n bytes have been read.
type capReader struct {
	r io.Reader
	n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n -= int64(n)
	if c.n < 0 {
		return n, errTooLarge
	}
	return n, err
}

// CheckCredentials verifies the login without reading any object.
func (c *Client) CheckCredentials(ctx context.Context) error {
	_, err := c.post(ctx, "core/check_credentials", nil)
	return err
}

// keyValue sends a numeric id as a number and anything else (an OQL SELECT)
// as a string.
func keyValue(key string) any {
	if n, err := strconv.ParseInt(key, 10, 64); err == nil {
		return n
	}
	return key
}

// Get runs core/get. key is an OQL SELECT or a numeric id. page is 0-based
// here and sent 1-based; limit 0 means no limit. Empty fields selects all
// attributes.
func (c *Client) Get(ctx context.Context, class, key string, fields []string, limit, page int) ([]Object, error) {
	output := "*"
	if len(fields) > 0 {
		output = strings.Join(fields, ",")
	}
	payload := map[string]any{
		"class":         class,
		"key":           keyValue(key),
		"output_fields": output,
	}
	if limit > 0 {
		payload["limit"] = limit
		payload["page"] = page + 1
	}
	r, err := c.post(ctx, "core/get", payload)
	if err != nil {
		return nil, err
	}
	objs := make([]Object, 0, len(r.Objects))
	for _, o := range r.Objects {
		objs = append(objs, Object{Class: o.Class, ID: string(o.Key), Fields: o.Fields})
	}
	return objs, nil
}

// GetAll pages through an OQL query until max objects are collected.
// truncated reports that more objects exist beyond max.
func (c *Client) GetAll(ctx context.Context, class, oql string, fields []string, max int) (objs []Object, truncated bool, err error) {
	for page := 0; ; page++ {
		batch, err := c.Get(ctx, class, oql, fields, pageSize, page)
		if err != nil {
			return nil, false, err
		}
		if len(objs) >= max {
			return objs[:max], len(batch) > 0, nil
		}
		objs = append(objs, batch...)
		if len(objs) > max {
			return objs[:max], true, nil
		}
		if len(batch) < pageSize {
			return objs, false, nil
		}
	}
}
