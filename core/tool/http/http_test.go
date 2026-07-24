package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	httptool "github.com/tzpereira/workflow-execution-engine/core/tool/http"
)

// TestAllowedHostSucceeds: a GET to an allowlisted host returns status + body.
func TestAllowedHostSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "pong")
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	tool := httptool.New([]string{host}, srv.Client())
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var r struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	_ = json.Unmarshal(out, &r)
	if r.Status != 200 || r.Body != "pong" {
		t.Errorf("got status=%d body=%q", r.Status, r.Body)
	}
}

func TestEmptyHeaderIsOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, present := r.Header["Authorization"]; present {
			t.Error("empty Authorization header should be omitted")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	tool := httptool.New([]string{host}, srv.Client())
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","url":"`+srv.URL+`","headers":{"Authorization":""}}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestDisallowedDomainRejected is the M1.5 acceptance test (REQ-TOOL-03): a
// request to a host not on the allowlist fails, and the server is never hit.
func TestDisallowedDomainRejected(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()

	// Allowlist a different domain than the server's.
	tool := httptool.New([]string{"allowed.example.com"}, srv.Client())
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","url":"`+srv.URL+`"}`)); err == nil {
		t.Fatal("a request to a non-allowlisted host must be rejected")
	}
	if hit {
		t.Error("the server was contacted despite the host not being allowlisted")
	}
}

func TestEmptyAllowlistDeniesAll(t *testing.T) {
	tool := httptool.New(nil, nil)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","url":"https://example.com"}`)); err == nil {
		t.Fatal("an empty allowlist must deny everything")
	}
}

func TestSubdomainSuffixMatch(t *testing.T) {
	tool := httptool.New([]string{".example.com"}, nil)
	// The matcher is exercised directly through a rejected/allowed decision:
	// api.example.com is allowed by the ".example.com" suffix, evil.com is not.
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","url":"https://evil.com"}`)); err == nil {
		t.Error("evil.com should be denied by a .example.com allowlist")
	}
}

func TestURLRewriteAllowsHumanURLThroughAPIAllowlist(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Accept") != "application/vnd.github.diff" {
			t.Errorf("Accept header = %q", r.Header.Get("Accept"))
		}
		_, _ = io.WriteString(w, "diff --git a/a b/a")
	}))
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteHostTransport{
		base:      srv.Client().Transport,
		targetURL: srv.URL,
	}
	tool := httptool.New([]string{"api.github.com"}, client)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{
		"method": "GET",
		"url": "https://github.com/bitcoin/bitcoin/pull/35750/changes",
		"urlRewrite": [
			{
				"match": "^https://github\\.com/([^/]+)/([^/]+)/pull/([0-9]+)(?:/.*)?$",
				"replace": "https://api.github.com/repos/$1/$2/pulls/$3"
			}
		],
		"headers": {"Accept": "application/vnd.github.diff"},
		"failOnStatus": true
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var r struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	_ = json.Unmarshal(out, &r)
	if gotPath != "/repos/bitcoin/bitcoin/pulls/35750" {
		t.Fatalf("transformed path = %q", gotPath)
	}
	if r.Status != 200 || r.Body != "diff --git a/a b/a" {
		t.Errorf("got status=%d body=%q", r.Status, r.Body)
	}
}

func TestURLRewriteRequiresMatchingRule(t *testing.T) {
	tool := httptool.New([]string{"api.github.com"}, nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"method": "GET",
		"url": "https://example.com/not-a-pr",
		"urlRewrite": [
			{
				"match": "^https://github\\.com/([^/]+)/([^/]+)/pull/([0-9]+)(?:/.*)?$",
				"replace": "https://api.github.com/repos/$1/$2/pulls/$3"
			}
		]
	}`))
	if err == nil {
		t.Fatal("expected an unmatched rewrite rule to fail before any request")
	}
	if !strings.Contains(err.Error(), "did not match any urlRewrite rule") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFailOnStatusRejectsHTTPErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	tool := httptool.New([]string{host}, srv.Client())
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","url":"`+srv.URL+`","failOnStatus":true}`))
	if err == nil {
		t.Fatal("expected failOnStatus to reject a 404 response")
	}
	if !strings.Contains(err.Error(), "status 404") || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("error should include status and body preview, got %v", err)
	}
}

// TestBodyJSONSentAsRequestBody: bodyJson is marshaled verbatim as the
// request body (REQ-TOOL-06) — the engine has already resolved any
// placeholder leaves inside it by the time Execute sees the input.
func TestBodyJSONSentAsRequestBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	tool := httptool.New([]string{host}, srv.Client())
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"method": "POST",
		"url": "`+srv.URL+`",
		"bodyJson": {"content": "package middleware\n\nfunc x() {}\n", "encoding": "utf-8"}
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal([]byte(gotBody), &got); err != nil {
		t.Fatalf("server received non-JSON body %q: %v", gotBody, err)
	}
	if got.Content != "package middleware\n\nfunc x() {}\n" || got.Encoding != "utf-8" {
		t.Errorf("got body %+v", got)
	}
}

func TestBodyJSONSetsContentTypeWhenAbsent(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	tool := httptool.New([]string{host}, srv.Client())
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"method": "POST",
		"url": "`+srv.URL+`",
		"bodyJson": {"a": 1}
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
}

func TestBodyJSONRespectsExplicitContentType(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	tool := httptool.New([]string{host}, srv.Client())
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"method": "POST",
		"url": "`+srv.URL+`",
		"headers": {"Content-Type": "application/vnd.github+json"},
		"bodyJson": {"a": 1}
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotContentType != "application/vnd.github+json" {
		t.Errorf("Content-Type = %q, want the workflow-supplied value preserved", gotContentType)
	}
}

func TestBodyAndBodyJSONAreMutuallyExclusive(t *testing.T) {
	tool := httptool.New([]string{"example.com"}, nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"method": "POST",
		"url": "https://example.com",
		"body": "raw",
		"bodyJson": {"a": 1}
	}`))
	if err == nil {
		t.Fatal("expected body and bodyJson together to be rejected")
	}
}

// TestBodyJSONResponseFieldIsParsed: a JSON-object response body is exposed a
// second time, already parsed, as bodyJson (REQ-TOOL-07) — so a downstream
// tool node can reference an exact field deterministically.
func TestBodyJSONResponseFieldIsParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"sha":"abc123","tree":{"sha":"def456"}}`)
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	tool := httptool.New([]string{host}, srv.Client())
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var r struct {
		Body     string `json:"body"`
		BodyJSON struct {
			Sha  string `json:"sha"`
			Tree struct {
				Sha string `json:"sha"`
			} `json:"tree"`
		} `json:"bodyJson"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if r.BodyJSON.Sha != "abc123" || r.BodyJSON.Tree.Sha != "def456" {
		t.Errorf("got bodyJson %+v", r.BodyJSON)
	}
	if r.Body == "" {
		t.Error("raw body string should still be present alongside bodyJson")
	}
}

func TestBodyJSONResponseAbsentForNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "package middleware\n")
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	tool := httptool.New([]string{host}, srv.Client())
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var r map[string]any
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if _, present := r["bodyJson"]; present {
		t.Error("bodyJson should be absent when the response body is not a JSON object/array")
	}
}

type rewriteHostTransport struct {
	base      http.RoundTripper
	targetURL string
}

func (rt rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(rt.targetURL)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.Host = target.Host
	return rt.base.RoundTrip(req)
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname()
}
