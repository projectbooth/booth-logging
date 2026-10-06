package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/projectbooth/booth-logging/internal/auth"
	"github.com/projectbooth/booth-logging/internal/loki"
)

// fakeVerifier maps a raw token to its claims; anything else is invalid.
type fakeVerifier map[string]*auth.Claims

func (f fakeVerifier) Verify(_ context.Context, raw string) (*auth.Claims, error) {
	if c, ok := f[raw]; ok {
		return c, nil
	}
	return nil, errors.New("invalid")
}

var tokens = fakeVerifier{
	"owner-acme":  {Subject: "alice", Groups: []string{"/workspaces/acme/owner"}},
	"editor-acme": {Subject: "bob", Groups: []string{"/workspaces/acme/editor"}},
	"viewer-acme": {Subject: "carol", Groups: []string{"/workspaces/acme/viewer"}},
	// ADR 0094: platform operators, by the /platform/operator claim — one who is only a viewer
	// in acme, and one who is also acme's owner.
	"operator-viewer-acme": {Subject: "ops", Groups: []string{"/workspaces/acme/viewer", "/platform/operator"}},
	"operator-owner-acme":  {Subject: "ops2", Groups: []string{"/workspaces/acme/owner", "/platform/operator"}},
	// ADR 0067's stopgap shape: an owner of a "platform" workspace. No longer an operator.
	"owner-platform": {Subject: "old", Groups: []string{"/workspaces/platform/owner"}},
}

// recordingLoki records requests and returns canned results.
type recordingLoki struct {
	queries  []loki.QueryRangeRequest
	entries  []loki.Entry
	labels   []string
	labelReq [2]time.Time
	selector string
	err      error
	readyErr error
}

func (r *recordingLoki) QueryRange(_ context.Context, req loki.QueryRangeRequest) ([]loki.Entry, error) {
	r.queries = append(r.queries, req)
	if r.err != nil {
		return nil, r.err
	}
	if len(r.entries) > req.Limit {
		return r.entries[:req.Limit], nil
	}
	return r.entries, nil
}

func (r *recordingLoki) LabelValues(_ context.Context, _ string, start, end time.Time, selector string) ([]string, error) {
	r.labelReq = [2]time.Time{start, end}
	r.selector = selector
	return r.labels, r.err
}

func (r *recordingLoki) Ready(context.Context) error { return r.readyErr }

var fixedNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

func newTestServer(l Loki) http.Handler {
	return NewRouter(Deps{Verifier: tokens, Loki: l, Retention: 14 * 24 * time.Hour, Now: func() time.Time { return fixedNow }})
}

func do(t *testing.T, h http.Handler, token, workspace, target string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if workspace != "" {
		req.Header.Set(auth.HeaderBoothWorkspace, workspace)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestAccess(t *testing.T) {
	h := newTestServer(&recordingLoki{})
	cases := []struct {
		name, token, ws string
		headers         []string
		want            int
	}{
		{"no token", "", "acme", nil, 401},
		{"bad token", "forged", "acme", nil, 401},
		{"no workspace header", "owner-acme", "", nil, 400},
		{"owner", "owner-acme", "acme", nil, 200},
		{"editor refused", "editor-acme", "acme", nil, 403},
		{"viewer refused", "viewer-acme", "acme", nil, 403},
		{"no role in that workspace", "owner-acme", "globex", nil, 403},
		// ADR 0041: a forged role header can't lift a viewer to owner...
		{"viewer with forged owner header", "viewer-acme", "acme", []string{auth.HeaderBoothRole, "owner"}, 403},
		// ...and the gateway narrowing an owner's role is honoured.
		{"owner narrowed by gateway", "owner-acme", "acme", []string{auth.HeaderBoothRole, "viewer"}, 403},
		// ADR 0094: an operator is admitted whatever their role in the active workspace...
		{"platform operator who is only a viewer here", "operator-viewer-acme", "acme", nil, 200},
		// ...but still needs a workspace context their token grants some role in.
		{"platform operator in a workspace they have no role in", "operator-viewer-acme", "globex", nil, 403},
		// ADR 0067's stopgap shape grants nothing special any more: just an ordinary owner.
		{"owner of a workspace named platform", "owner-platform", "platform", nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/logs", "/api/modules", "/api/config"} {
				if rec := do(t, h, tc.token, tc.ws, path, tc.headers...); rec.Code != tc.want {
					t.Errorf("%s: status %d, want %d (%s)", path, rec.Code, tc.want, rec.Body)
				}
			}
		})
	}
}

func TestHealth(t *testing.T) {
	l := &recordingLoki{}
	h := newTestServer(l)
	if rec := do(t, h, "", "", "/healthz"); rec.Code != 200 {
		t.Errorf("healthz with Loki ready: %d", rec.Code)
	}
	l.readyErr = errors.New("Ingester not ready")
	rec := do(t, h, "", "", "/healthz")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "Ingester not ready") {
		t.Errorf("healthz with Loki down: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "", "", "/livez"); rec.Code != 200 {
		t.Errorf("livez must not depend on Loki: %d", rec.Code)
	}
}

func TestLogs_BuildsQueryFromFilters(t *testing.T) {
	l := &recordingLoki{}
	h := newTestServer(l) // an operator's query: this is about filters, not scoping (TestScope)
	q := url.Values{"module": {"storage", "catalog"}, "level": {"error", "WARN"}, "q": {"db down"}, "limit": {"50"},
		"start": {"2026-09-22T10:00:00Z"}, "end": {"1790078400000000000"}}
	rec := do(t, h, "operator-owner-acme", "acme", "/api/logs?"+q.Encode())
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := l.queries[0]
	want := `{module=~"catalog|storage"} |~ "(?i)db down" | detected_level=~"critical|error|fatal|warn"`
	if got.Query != want {
		t.Errorf("query\n got %s\nwant %s", got.Query, want)
	}
	if got.Limit != 50 || !got.Start.Equal(time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)) || got.End.UnixNano() != 1790078400000000000 {
		t.Errorf("request = %+v", got)
	}
	if resp := decode[LogsResponse](t, rec); resp.Query != want || resp.Entries == nil {
		t.Errorf("response should echo the query and have a non-null entries array: %+v", resp)
	}
}

func TestLogs_Defaults(t *testing.T) {
	l := &recordingLoki{}
	h := newTestServer(l) // an operator's query: this is about filters, not scoping (TestScope)
	if rec := do(t, h, "operator-owner-acme", "acme", "/api/logs"); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	got := l.queries[0]
	if got.Limit != DefaultLimit || !got.End.Equal(fixedNow) || !got.Start.Equal(fixedNow.Add(-time.Hour)) || got.Query != `{module=~".+"}` {
		t.Errorf("defaults = %+v", got)
	}
	// Only an end: a one-hour window ending there.
	do(t, h, "operator-owner-acme", "acme", "/api/logs?end=2026-09-01T00:00:00Z")
	if got := l.queries[1]; got.End.Sub(got.Start) != time.Hour {
		t.Errorf("end-only window = %v", got.End.Sub(got.Start))
	}
}

func TestLogs_Validation(t *testing.T) {
	h := newTestServer(&recordingLoki{})
	for _, target := range []string{
		"/api/logs?level=verbose",
		`/api/logs?module=` + url.QueryEscape(`storage"}`),
		"/api/logs?limit=0",
		"/api/logs?limit=1001",
		"/api/logs?limit=ten",
		"/api/logs?start=yesterday",
		"/api/logs?end=99999999999999999999",
		"/api/logs?start=2026-09-22T12:00:00Z&end=2026-09-22T11:00:00Z",
		"/api/logs?start=2026-09-01T00:00:00Z&end=2026-09-22T00:00:00Z", // 21 days > 14-day cap
		"/api/logs?q=" + url.QueryEscape(strings.Repeat("x", 501)),
	} {
		rec := do(t, h, "owner-acme", "acme", target)
		if rec.Code != 400 {
			t.Errorf("%s: %d, want 400", target, rec.Code)
			continue
		}
		if e := decode[map[string]string](t, rec); e["error"] == "" || e["field"] == "" {
			t.Errorf("%s: error body %v should name the problem and field", target, e)
		}
	}
}

func TestLogs_ResponseAndCursor(t *testing.T) {
	l := &recordingLoki{entries: []loki.Entry{
		{Timestamp: 3_000_000_001, Line: "c", Labels: map[string]string{"module": "storage", "pod": "p", "namespace": "ns", "container": "c1", "stream": "stderr", "detected_level": "fatal"}},
		{Timestamp: 2_000_000_000, Line: "b", Labels: map[string]string{"module": "storage"}},
		{Timestamp: 1_000_000_000, Line: "a", Labels: map[string]string{"module": "storage", "detected_level": "trace"}},
	}}
	h := newTestServer(l)

	resp := decode[LogsResponse](t, do(t, h, "owner-acme", "acme", "/api/logs?limit=2"))
	if len(resp.Entries) != 2 || resp.NextCursor != "2000000000" {
		t.Fatalf("full page should carry a cursor at its oldest line: %+v", resp)
	}
	first := resp.Entries[0]
	if first.Timestamp != "1970-01-01T00:00:03.000000001Z" || first.Cursor != "3000000001" || first.Level != "error" ||
		first.Pod != "p" || first.Namespace != "ns" || first.Container != "c1" || first.Stream != "stderr" || first.Module != "storage" {
		t.Errorf("entry = %+v", first)
	}
	if resp.Entries[1].Level != "unknown" {
		t.Errorf("no detected_level should read as unknown, got %q", resp.Entries[1].Level)
	}

	resp = decode[LogsResponse](t, do(t, h, "owner-acme", "acme", "/api/logs?limit=5"))
	if len(resp.Entries) != 3 || resp.NextCursor != "" || resp.Entries[2].Level != "debug" {
		t.Errorf("a short page has no cursor: %+v", resp)
	}
}

func TestLogs_LokiFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"rejected query": {&loki.Error{Status: 400, Message: "too many streams"}, 502},
		"timeout":        {context.DeadlineExceeded, 504},
		"unreachable":    {errors.New("dial tcp: connection refused"), 503},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, newTestServer(&recordingLoki{err: tc.err}), "owner-acme", "acme", "/api/logs")
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
			if name == "rejected query" && !strings.Contains(rec.Body.String(), "too many streams") {
				t.Errorf("Loki's message should reach the user: %s", rec.Body)
			}
		})
	}
}

func TestModules(t *testing.T) {
	l := &recordingLoki{labels: []string{"catalog", "storage"}}
	h := newTestServer(l)
	rec := do(t, h, "owner-acme", "acme", "/api/modules")
	if rec.Code != 200 || rec.Body.String() != `{"modules":["catalog","storage"]}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !l.labelReq[1].Equal(fixedNow) || !l.labelReq[0].Equal(fixedNow.Add(-14*24*time.Hour)) {
		t.Errorf("modules should default to the whole retention window, got %v", l.labelReq)
	}
	// The list isn't capped by MaxQueryRange (label lookups are index-only and cheap).
	if rec := do(t, h, "owner-acme", "acme", "/api/modules?start=2026-01-01T00:00:00Z"); rec.Code != 200 {
		t.Errorf("%d", rec.Code)
	}
}

func TestConfig(t *testing.T) {
	h := NewRouter(Deps{Verifier: tokens, Loki: &recordingLoki{}, Retention: 336 * time.Hour, MaxQueryRange: 24 * time.Hour})
	c := decode[ConfigResponse](t, do(t, h, "owner-acme", "acme", "/api/config"))
	if c.RetentionSeconds != 336*3600 || c.MaxQueryRangeSeconds != 24*3600 || c.MaxLimit != MaxLimit || strings.Join(c.Levels, ",") != "error,warn,info,debug,unknown" {
		t.Errorf("config = %+v", c)
	}
}

// ADR 0077 with ADR 0094's operators: a platform operator queries platform-wide; every other
// owner's queries are pinned to their active workspace — from the verified identity, whatever
// the request says.
func TestScope(t *testing.T) {
	for _, tc := range []struct {
		name, token, ws string
		wantPin         string // "" = platform-wide
		wantScope       string
	}{
		{"an owner is scoped to their workspace", "owner-acme", "acme", "acme", "workspace"},
		{"ADR 0067's old operator shape is just a scoped owner now", "owner-platform", "platform", "platform", "workspace"},
		{"a platform operator is platform-wide", "operator-viewer-acme", "acme", "", "platform"},
		{"a platform operator who is also an owner is platform-wide", "operator-owner-acme", "acme", "", "platform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &recordingLoki{labels: []string{"notebooks"}}
			h := newTestServer(l)

			// A client-supplied workspace parameter or matcher is ignored, not honoured.
			if rec := do(t, h, tc.token, tc.ws, "/api/logs?workspace=globex&module=notebooks"); rec.Code != 200 {
				t.Fatalf("logs: %d %s", rec.Code, rec.Body)
			}
			want := `{module=~"notebooks"}`
			if tc.wantPin != "" {
				want = `{module=~"notebooks", workspace="` + tc.wantPin + `"}`
			}
			if got := l.queries[0].Query; got != want {
				t.Errorf("query = %s, want %s", got, want)
			}

			do(t, h, tc.token, tc.ws, "/api/modules")
			wantSel := ""
			if tc.wantPin != "" {
				wantSel = `{module=~".+", workspace="` + tc.wantPin + `"}`
			}
			if l.selector != wantSel {
				t.Errorf("modules selector = %q, want %q", l.selector, wantSel)
			}

			c := decode[ConfigResponse](t, do(t, h, tc.token, tc.ws, "/api/config"))
			if c.Scope != tc.wantScope || c.Workspace != tc.wantPin {
				t.Errorf("config scope = %q/%q, want %q/%q", c.Scope, c.Workspace, tc.wantScope, tc.wantPin)
			}
		})
	}
}

// A request that reaches a handler with no scope attached is refused, never treated as
// platform-wide.
func TestScope_MissingFailsClosed(t *testing.T) {
	if ws, ok := pin(context.Background()); ok || ws != "" {
		t.Fatalf("pin(no scope) = %q, %v; want refusal", ws, ok)
	}
	s := &server{Deps: Deps{Loki: &recordingLoki{}, Retention: time.Hour, MaxQueryRange: time.Hour, Now: time.Now}}
	for name, h := range map[string]http.HandlerFunc{"logs": s.handleLogs, "modules": s.handleModules} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without a scope: %d, want 403", name, rec.Code)
		}
	}
}
