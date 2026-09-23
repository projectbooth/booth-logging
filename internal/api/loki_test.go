package api

import (
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/projectbooth/booth-logging/internal/loki"
	"github.com/projectbooth/booth-logging/internal/loki/lokitest"
)

// The viewer's whole query path — HTTP request → filter validation → LogQL → a real Loki →
// response — against lines pushed the way the collector ships them.
func TestRealLoki_ViewerQueryPath(t *testing.T) {
	lokiURL := lokitest.URL(t)
	mod := lokitest.UniqueModule(t)
	other := lokitest.UniqueModule(t)
	now := time.Now()
	at := func(s int) time.Time { return now.Add(time.Duration(s-60) * time.Second) }

	lokitest.Push(t, lokiURL, map[string]string{"module": mod, "namespace": "booth-" + mod, "pod": "p1", "container": "app", "stream": "stdout"},
		lokitest.Line{At: at(1), Text: `{"level":"info","msg":"started"}`},
		lokitest.Line{At: at(2), Text: `{"level":"error","msg":"Database DOWN"}`},
		lokitest.Line{At: at(3), Text: `level=warn msg="slow query" took=3s`},
		lokitest.Line{At: at(4), Text: `a literal "} or {namespace="x"} line`},
		lokitest.Line{At: at(5), Text: `plain text with no level`},
	)
	lokitest.Push(t, lokiURL, map[string]string{"module": other}, lokitest.Line{At: at(2), Text: `{"level":"error","msg":"database down elsewhere"}`})

	h := NewRouter(Deps{Verifier: tokens, Loki: loki.New(lokiURL, nil), Retention: 14 * 24 * time.Hour})
	query := func(v url.Values) LogsResponse {
		t.Helper()
		v.Set("start", strconv.FormatInt(now.Add(-2*time.Minute).UnixNano(), 10))
		if v.Get("end") == "" {
			v.Set("end", strconv.FormatInt(now.UnixNano(), 10))
		}
		rec := do(t, h, "owner-acme", "acme", "/api/logs?"+v.Encode())
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", v.Encode(), rec.Code, rec.Body)
		}
		return decode[LogsResponse](t, rec)
	}
	lines := func(r LogsResponse) (out []string) {
		for _, e := range r.Entries {
			out = append(out, e.Line)
		}
		return out
	}

	lokitest.Eventually(t, 15*time.Second, func() bool {
		return len(query(url.Values{"module": {mod}}).Entries) == 5
	})

	t.Run("module filter and labels", func(t *testing.T) {
		r := query(url.Values{"module": {mod}})
		e := r.Entries[0]
		if e.Module != mod || e.Namespace != "booth-"+mod || e.Pod != "p1" || e.Container != "app" || e.Stream != "stdout" || e.Level != "unknown" {
			t.Errorf("newest entry = %+v", e)
		}
	})

	t.Run("case-insensitive search across modules", func(t *testing.T) {
		r := query(url.Values{"module": {mod, other}, "q": {"database down"}})
		if len(r.Entries) != 2 {
			t.Errorf("got %q", lines(r))
		}
	})

	t.Run("severity buckets", func(t *testing.T) {
		r := query(url.Values{"module": {mod}, "level": {"error", "warn"}})
		got := lines(r)
		if len(got) != 2 || r.Entries[0].Level != "warn" || r.Entries[1].Level != "error" {
			t.Errorf("got %+v", r.Entries)
		}
		if r := query(url.Values{"module": {mod}, "level": {"unknown"}}); len(r.Entries) != 2 {
			t.Errorf("unknown bucket: %q", lines(r))
		}
	})

	t.Run("a search can't rewrite the query", func(t *testing.T) {
		r := query(url.Values{"module": {mod}, "q": {`"} or {namespace="x"}`}})
		if got := lines(r); len(got) != 1 || got[0] != `a literal "} or {namespace="x"} line` {
			t.Errorf("got %q", got)
		}
	})

	t.Run("paging with the cursor visits every line once", func(t *testing.T) {
		var seen []string
		v := url.Values{"module": {mod}, "limit": {"2"}}
		for range 5 {
			r := query(v)
			seen = append(seen, lines(r)...)
			if r.NextCursor == "" {
				break
			}
			v.Set("end", r.NextCursor)
		}
		if len(seen) != 5 || seen[0] != "plain text with no level" || seen[4] != `{"level":"info","msg":"started"}` {
			t.Errorf("paged lines = %q", seen)
		}
	})

	t.Run("modules list", func(t *testing.T) {
		rec := do(t, h, "owner-acme", "acme", "/api/modules")
		m := decode[map[string][]string](t, rec)
		found := 0
		for _, v := range m["modules"] {
			if v == mod || v == other {
				found++
			}
		}
		if found != 2 {
			t.Errorf("modules = %q", m["modules"])
		}
	})
}
