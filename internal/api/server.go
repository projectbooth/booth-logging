// Package api is booth-logging's HTTP surface: the read-only query API its native viewer
// calls (through booth-core's gateway at /modules/logging/api/...), plus health.
//
// There is no ingestion route and there never should be one: logs get into Loki from the
// node-level collector tailing container stdout/stderr (ADR 0022). This service only turns
// a signed-in owner's structured filter into LogQL and hands back what Loki returns.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/projectbooth/booth-logging/internal/auth"
	"github.com/projectbooth/booth-logging/internal/logql"
	"github.com/projectbooth/booth-logging/internal/loki"
)

const (
	// DefaultLimit and MaxLimit bound how many lines one request returns. The viewer pages
	// with the returned cursor rather than asking for everything at once.
	DefaultLimit = 200
	MaxLimit     = 1000

	// defaultWindow is the time range used when a request names no start.
	defaultWindow = time.Hour

	healthTimeout = 3 * time.Second
)

// Loki is the subset of *loki.Client the API uses, so handlers can be tested against a
// fake as well as a real Loki.
type Loki interface {
	QueryRange(ctx context.Context, req loki.QueryRangeRequest) ([]loki.Entry, error)
	LabelValues(ctx context.Context, label string, start, end time.Time) ([]string, error)
	Ready(ctx context.Context) error
}

// AccessPolicy decides who may read logs. Logs are cluster-wide, not workspace-scoped —
// one booth-storage pod serves every workspace and its lines mention all of them — so a
// workspace role alone can't scope what a caller sees. See docs/decisions/0002.
type AccessPolicy struct {
	// Workspaces, if non-empty, limits log access to owners of these workspaces (e.g. a
	// dedicated "platform" workspace). Empty means an owner of any workspace may read all
	// logs, which is only appropriate when every workspace owner is trusted operator staff.
	Workspaces []string
}

// Allows reports whether id may read logs, and if not, why.
func (p AccessPolicy) Allows(id auth.Identity) (bool, string) {
	if !id.IsOwner() {
		return false, "reading logs requires the owner role in the active workspace"
	}
	if len(p.Workspaces) > 0 && !slices.Contains(p.Workspaces, id.Workspace) {
		return false, "this deployment only lets owners of designated workspaces read logs; switch to one of them or ask your operator"
	}
	return true, ""
}

// Deps is everything the router needs.
type Deps struct {
	Verifier auth.TokenVerifier
	Loki     Loki
	Access   AccessPolicy
	// Retention is how long Loki keeps logs (the chart's loki.retentionDays). Reported to
	// the UI, and the default window for listing modules.
	Retention time.Duration
	// MaxQueryRange caps end-start on a single query. Defaults to Retention.
	MaxQueryRange time.Duration
	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time
}

type server struct {
	Deps
}

// NewRouter builds the HTTP handler.
func NewRouter(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.MaxQueryRange <= 0 {
		d.MaxQueryRange = d.Retention
	}
	s := &server{Deps: d}
	r := chi.NewRouter()

	// Unauthenticated. /healthz is the manifest's healthCheckPath and the readiness probe:
	// the viewer is useless without Loki, so it reports Loki's readiness. /livez (liveness)
	// deliberately does not — restarting this pod can't fix Loki.
	r.Get("/healthz", s.handleHealthz)
	r.Get("/livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(d.Verifier))
		r.Use(s.requireAccess)
		r.Get("/api/config", s.handleConfig)
		r.Get("/api/modules", s.handleModules)
		r.Get("/api/logs", s.handleLogs)
	})
	return r
}

func (s *server) requireAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.FromContext(r.Context())
		if !ok {
			auth.WriteError(w, http.StatusUnauthorized, "no identity")
			return
		}
		if allowed, why := s.Access.Allows(id); !allowed {
			auth.WriteError(w, http.StatusForbidden, why)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	if err := s.Loki.Ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": "loki not ready: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ConfigResponse tells the UI what it can ask for.
type ConfigResponse struct {
	RetentionSeconds     int64    `json:"retentionSeconds"`
	MaxQueryRangeSeconds int64    `json:"maxQueryRangeSeconds"`
	MaxLimit             int      `json:"maxLimit"`
	Levels               []string `json:"levels"`
}

func (s *server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	levels := make([]string, len(logql.Levels))
	for i, l := range logql.Levels {
		levels[i] = string(l)
	}
	writeJSON(w, http.StatusOK, ConfigResponse{
		RetentionSeconds:     int64(s.Retention / time.Second),
		MaxQueryRangeSeconds: int64(s.MaxQueryRange / time.Second),
		MaxLimit:             MaxLimit,
		Levels:               levels,
	})
}

// handleModules lists module label values seen in [start, end); by default, the whole
// retention window, so every module with any retained logs is offered.
func (s *server) handleModules(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	start, end, ok := s.timeRange(w, r, now.Add(-s.Retention), now, false)
	if !ok {
		return
	}
	modules, err := s.Loki.LabelValues(r.Context(), logql.LabelModule, start, end)
	if err != nil {
		writeLokiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": modules})
}

// LogEntry is one line as the viewer shows it.
type LogEntry struct {
	// Timestamp is RFC 3339 with nanoseconds, for display.
	Timestamp string `json:"timestamp"`
	// Cursor is the same instant as Unix nanoseconds, as a string (it doesn't fit a JS
	// number). Passing a line's cursor as `end` continues strictly before it.
	Cursor    string `json:"cursor"`
	Line      string `json:"line"`
	Level     string `json:"level"`
	Module    string `json:"module"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
	Stream    string `json:"stream,omitempty"`
}

// LogsResponse is GET /api/logs's body.
type LogsResponse struct {
	Entries []LogEntry `json:"entries"`
	// NextCursor, when present, fetches the next (older) page when passed as `end` with
	// the same filters and start.
	NextCursor string `json:"nextCursor,omitempty"`
	// Query is the LogQL that ran — shown in the UI so a user can take it to Grafana.
	Query string `json:"query"`
}

// handleLogs: GET /api/logs?module=&module=&level=&q=&start=&end=&limit=
//
// Newest first. start/end are RFC 3339 or Unix nanoseconds; start is inclusive, end is
// exclusive (Loki's semantics, which is what makes the cursor work).
func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := logql.Filter{Modules: q["module"], Search: q.Get("q")}
	for _, raw := range q["level"] {
		l, err := logql.ParseLevel(raw)
		if err != nil {
			writeFieldError(w, http.StatusBadRequest, err.Error(), "level")
			return
		}
		filter.Levels = append(filter.Levels, l)
	}
	if err := filter.Validate(); err != nil {
		writeFieldError(w, http.StatusBadRequest, err.Error(), "filter")
		return
	}

	limit := DefaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxLimit {
			writeFieldError(w, http.StatusBadRequest, "limit must be an integer from 1 to "+strconv.Itoa(MaxLimit), "limit")
			return
		}
		limit = n
	}

	now := s.Now()
	start, end, ok := s.timeRange(w, r, now.Add(-defaultWindow), now, true)
	if !ok {
		return
	}

	query := filter.Query()
	entries, err := s.Loki.QueryRange(r.Context(), loki.QueryRangeRequest{Query: query, Start: start, End: end, Limit: limit})
	if err != nil {
		writeLokiError(w, err)
		return
	}

	resp := LogsResponse{Entries: make([]LogEntry, 0, len(entries)), Query: query}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, LogEntry{
			Timestamp: time.Unix(0, e.Timestamp).UTC().Format(time.RFC3339Nano),
			Cursor:    strconv.FormatInt(e.Timestamp, 10),
			Line:      e.Line,
			Level:     string(logql.BucketOf(e.Labels[logql.DetectedLevel])),
			Module:    e.Labels[logql.LabelModule],
			Namespace: e.Labels[logql.LabelNamespace],
			Pod:       e.Labels[logql.LabelPod],
			Container: e.Labels[logql.LabelContainer],
			Stream:    e.Labels[logql.LabelStream],
		})
	}
	// A full page means there may be more. (Lines sharing the exact nanosecond of the page
	// boundary can be skipped by an exclusive end; with nanosecond timestamps from the
	// container runtime that is vanishingly rare and not worth a stateful cursor.)
	if len(entries) == limit {
		resp.NextCursor = strconv.FormatInt(entries[len(entries)-1].Timestamp, 10)
	}
	writeJSON(w, http.StatusOK, resp)
}

// timeRange parses start/end with the given defaults and, if capped, enforces
// MaxQueryRange. It writes the error response itself and returns ok=false on failure.
func (s *server) timeRange(w http.ResponseWriter, r *http.Request, defStart, defEnd time.Time, capped bool) (time.Time, time.Time, bool) {
	q := r.URL.Query()
	end, err := parseTime(q.Get("end"), defEnd)
	if err != nil {
		writeFieldError(w, http.StatusBadRequest, "end: "+err.Error(), "end")
		return time.Time{}, time.Time{}, false
	}
	// With only an end given, keep the default window's length, ending there.
	start, err := parseTime(q.Get("start"), end.Add(-defEnd.Sub(defStart)))
	if err != nil {
		writeFieldError(w, http.StatusBadRequest, "start: "+err.Error(), "start")
		return time.Time{}, time.Time{}, false
	}
	if !start.Before(end) {
		writeFieldError(w, http.StatusBadRequest, "start must be before end", "start")
		return time.Time{}, time.Time{}, false
	}
	if capped && s.MaxQueryRange > 0 && end.Sub(start) > s.MaxQueryRange {
		writeFieldError(w, http.StatusBadRequest, "time range is longer than the maximum of "+s.MaxQueryRange.String(), "start")
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// parseTime accepts RFC 3339 (any precision) or an integer count of Unix nanoseconds.
func parseTime(v string, def time.Time) (time.Time, error) {
	if v == "" {
		return def, nil
	}
	if strings.Trim(v, "0123456789") == "" {
		ns, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return time.Time{}, errors.New("not a valid nanosecond timestamp")
		}
		return time.Unix(0, ns), nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, errors.New("want RFC 3339 or Unix nanoseconds")
	}
	return t, nil
}

// writeLokiError maps a failure talking to Loki onto a gateway-style status: the request
// was fine, the backend behind this service wasn't.
func writeLokiError(w http.ResponseWriter, err error) {
	var lerr *loki.Error
	var nerr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout()):
		auth.WriteError(w, http.StatusGatewayTimeout, "the log store took too long to answer; try a shorter time range or a narrower filter")
	case errors.As(err, &lerr):
		auth.WriteError(w, http.StatusBadGateway, "the log store rejected the query: "+lerr.Message)
	default:
		auth.WriteError(w, http.StatusServiceUnavailable, "the log store is unavailable")
	}
}

func writeFieldError(w http.ResponseWriter, status int, message, field string) {
	writeJSON(w, status, map[string]string{"error": message, "field": field})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
