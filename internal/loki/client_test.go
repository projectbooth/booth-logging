package loki_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/projectbooth/booth-logging/internal/logql"
	"github.com/projectbooth/booth-logging/internal/loki"
	"github.com/projectbooth/booth-logging/internal/loki/lokitest"
)

// ---- against a real Loki -----------------------------------------------------------

func TestRealLoki_QueryRangeMergesStreamsNewestFirst(t *testing.T) {
	url := lokitest.URL(t)
	c := loki.New(url, nil)
	mod := lokitest.UniqueModule(t)
	now := time.Now()

	lokitest.Push(t, url, map[string]string{"module": mod, "pod": "a"},
		lokitest.Line{At: now.Add(-3 * time.Second), Text: `{"level":"error","msg":"first"}`},
		lokitest.Line{At: now.Add(-1 * time.Second), Text: `{"level":"info","msg":"third"}`},
	)
	lokitest.Push(t, url, map[string]string{"module": mod, "pod": "b"},
		lokitest.Line{At: now.Add(-2 * time.Second), Text: "level=warn msg=second"},
	)

	var got []loki.Entry
	lokitest.Eventually(t, 15*time.Second, func() bool {
		var err error
		got, err = c.QueryRange(context.Background(), loki.QueryRangeRequest{
			Query: logql.Filter{Modules: []string{mod}}.Query(),
			Start: now.Add(-time.Minute), End: now.Add(time.Second), Limit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		return len(got) == 3
	})

	var lines, pods, levels []string
	for _, e := range got {
		lines = append(lines, e.Line)
		pods = append(pods, e.Labels["pod"])
		levels = append(levels, e.Labels[logql.DetectedLevel])
	}
	if !strings.Contains(lines[0], "third") || !strings.Contains(lines[1], "second") || !strings.Contains(lines[2], "first") {
		t.Errorf("not merged newest-first across streams: %q", lines)
	}
	if !slices.Equal(pods, []string{"a", "b", "a"}) {
		t.Errorf("stream labels not carried per entry: %q", pods)
	}
	// Loki's own level discovery (limits_config.discover_log_levels) is what the viewer's
	// severity filter relies on — JSON and logfmt both.
	if !slices.Equal(levels, []string{"info", "warn", "error"}) {
		t.Errorf("detected_level = %q, want [info warn error]", levels)
	}
}

func TestRealLoki_LimitAndExclusiveEnd(t *testing.T) {
	url := lokitest.URL(t)
	c := loki.New(url, nil)
	mod := lokitest.UniqueModule(t)
	now := time.Now().Truncate(time.Second)

	var lines []lokitest.Line
	for i := range 5 {
		lines = append(lines, lokitest.Line{At: now.Add(time.Duration(i-5) * time.Second), Text: "n" + string(rune('0'+i))})
	}
	lokitest.Push(t, url, map[string]string{"module": mod}, lines...)
	req := loki.QueryRangeRequest{Query: logql.Filter{Modules: []string{mod}}.Query(), Start: now.Add(-time.Minute), End: now, Limit: 2}

	var page []loki.Entry
	lokitest.Eventually(t, 15*time.Second, func() bool {
		all, err := c.QueryRange(context.Background(), loki.QueryRangeRequest{Query: req.Query, Start: req.Start, End: req.End, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return len(all) == 5
	})
	page, err := c.QueryRange(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Line != "n4" || page[1].Line != "n3" {
		t.Fatalf("first page = %+v", page)
	}
	// The API's cursor: the next page ends (exclusively) at the oldest line seen.
	req.End = time.Unix(0, page[1].Timestamp)
	page, err = c.QueryRange(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Line != "n2" || page[1].Line != "n1" {
		t.Fatalf("second page = %+v (end must be exclusive for the cursor to work)", page)
	}
}

func TestRealLoki_LabelValuesAndReady(t *testing.T) {
	url := lokitest.URL(t)
	c := loki.New(url, nil)
	mod := lokitest.UniqueModule(t)
	lokitest.Push(t, url, map[string]string{"module": mod}, lokitest.Line{At: time.Now(), Text: "hello"})

	lokitest.Eventually(t, 15*time.Second, func() bool {
		vals, err := c.LabelValues(context.Background(), "module", time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(vals, mod) && slices.IsSorted(vals)
	})
	if err := c.Ready(context.Background()); err != nil {
		t.Errorf("Ready: %v", err)
	}
}

func TestRealLoki_BadQuerySurfacesLokisMessage(t *testing.T) {
	c := loki.New(lokitest.URL(t), nil)
	_, err := c.QueryRange(context.Background(), loki.QueryRangeRequest{Query: `{module=~""}`, Start: time.Now().Add(-time.Minute), End: time.Now(), Limit: 1})
	var lerr *loki.Error
	if !errors.As(err, &lerr) || lerr.Status != http.StatusBadRequest || lerr.Message == "" {
		t.Fatalf("err = %v, want a *loki.Error 400 with Loki's message", err)
	}
}

// ---- response shapes a real Loki doesn't produce on demand --------------------------

func fakeLoki(t *testing.T, body string, status int) *loki.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("direction") != "backward" {
			t.Errorf("direction = %q, want backward", r.URL.Query().Get("direction"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return loki.New(srv.URL, nil)
}

func TestQueryRange_StructuredMetadataElement(t *testing.T) {
	// Loki returns a third element per value when structured metadata isn't folded into
	// the stream: a flat map, or (with categorize-labels) a nested one.
	c := fakeLoki(t, `{"data":{"resultType":"streams","result":[
		{"stream":{"module":"m"},"values":[
			["3","flat",{"detected_level":"warn"}],
			["2","nested",{"structuredMetadata":{"detected_level":"error"}}],
			["1","odd",["not","a","map"]]
		]}]}}`, 200)
	got, err := c.QueryRange(context.Background(), loki.QueryRangeRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Labels["detected_level"] != "warn" || got[1].Labels["detected_level"] != "error" || got[2].Labels["module"] != "m" {
		t.Errorf("got %+v", got)
	}
}

func TestQueryRange_TruncatesToLimitAcrossStreams(t *testing.T) {
	c := fakeLoki(t, `{"data":{"resultType":"streams","result":[
		{"stream":{"s":"a"},"values":[["5","a5"],["1","a1"]]},
		{"stream":{"s":"b"},"values":[["4","b4"],["3","b3"]]}]}}`, 200)
	got, err := c.QueryRange(context.Background(), loki.QueryRangeRequest{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range got {
		lines = append(lines, e.Line)
	}
	if !slices.Equal(lines, []string{"a5", "b4", "b3"}) {
		t.Errorf("lines = %q", lines)
	}
}

func TestQueryRange_Errors(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"http error":        {"parse error at line 1", 400},
		"matrix not stream": {`{"data":{"resultType":"matrix","result":[]}}`, 200},
		"bad timestamp":     {`{"data":{"resultType":"streams","result":[{"stream":{},"values":[["x","l"]]}]}}`, 200},
		"short value":       {`{"data":{"resultType":"streams","result":[{"stream":{},"values":[["1"]]}]}}`, 200},
		"not json":          {`<html>`, 200},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fakeLoki(t, tc.body, tc.status).QueryRange(context.Background(), loki.QueryRangeRequest{Limit: 1}); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestReady_NotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Ingester not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	err := loki.New(srv.URL, nil).Ready(context.Background())
	var lerr *loki.Error
	if !errors.As(err, &lerr) || lerr.Status != 503 || !strings.Contains(lerr.Message, "Ingester") {
		t.Errorf("err = %v", err)
	}
	if err := loki.New("http://127.0.0.1:1", nil).Ready(context.Background()); err == nil {
		t.Error("unreachable Loki reported ready")
	}
}
