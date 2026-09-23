// Package loki is a minimal client for the three Loki HTTP endpoints the viewer needs:
// query_range (log lines), label values (which modules exist) and ready (health).
//
// It is read-only on purpose. Logs reach Loki from the node-level collector, never from
// this service or any module (ADR 0022), so there is no push method here to misuse.
package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client talks to one Loki instance. Loki runs with auth_enabled: false inside the
// release (the chart's NetworkPolicy is what keeps other pods off it), so no tenant header
// is sent.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for the Loki at baseURL (e.g. http://booth-logging-loki:3100).
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

// Entry is one log line with the stream labels it arrived under.
type Entry struct {
	// Timestamp is nanoseconds since the Unix epoch, as Loki stores it. Kept as an
	// integer (not time.Time) because it doubles as the paging cursor and must round-trip
	// exactly.
	Timestamp int64
	Line      string
	// Labels holds the stream labels plus any structured metadata Loki returned alongside
	// (detected_level lives there).
	Labels map[string]string
}

// QueryRangeRequest is a backward (newest-first) log query.
type QueryRangeRequest struct {
	Query string
	Start time.Time // inclusive
	End   time.Time // exclusive
	Limit int
}

// Error is a non-2xx response from Loki, carrying its message so a bad query surfaces as
// something readable rather than "502".
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("loki: HTTP %d: %s", e.Status, e.Message) }

// QueryRange runs a log query and returns up to req.Limit entries, newest first, merged
// across every stream Loki returned.
func (c *Client) QueryRange(ctx context.Context, req QueryRangeRequest) ([]Entry, error) {
	q := url.Values{}
	q.Set("query", req.Query)
	q.Set("start", strconv.FormatInt(req.Start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(req.End.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(req.Limit))
	q.Set("direction", "backward")

	var body struct {
		Data struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				// Each value is [ts, line] or, with structured metadata that Loki didn't
				// fold into the stream labels, [ts, line, {metadata}].
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/loki/api/v1/query_range", q, &body); err != nil {
		return nil, err
	}
	if body.Data.ResultType != "streams" {
		return nil, fmt.Errorf("loki: expected a streams result, got %q", body.Data.ResultType)
	}

	var entries []Entry
	for _, s := range body.Data.Result {
		for _, v := range s.Values {
			e, err := decodeValue(s.Stream, v)
			if err != nil {
				return nil, err
			}
			entries = append(entries, e)
		}
	}
	// Loki's limit is per query, but results come back grouped by stream; the viewer wants
	// one timeline. Stable, so equal timestamps keep Loki's order.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp > entries[j].Timestamp })
	if len(entries) > req.Limit {
		entries = entries[:req.Limit]
	}
	return entries, nil
}

func decodeValue(stream map[string]string, v []json.RawMessage) (Entry, error) {
	if len(v) < 2 {
		return Entry{}, fmt.Errorf("loki: malformed value (%d elements)", len(v))
	}
	var tsStr, line string
	if err := json.Unmarshal(v[0], &tsStr); err != nil {
		return Entry{}, fmt.Errorf("loki: malformed timestamp: %w", err)
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("loki: malformed timestamp %q: %w", tsStr, err)
	}
	if err := json.Unmarshal(v[1], &line); err != nil {
		return Entry{}, fmt.Errorf("loki: malformed line: %w", err)
	}
	labels := make(map[string]string, len(stream)+1)
	for k, val := range stream {
		labels[k] = val
	}
	if len(v) > 2 {
		// Loki's "categorized labels" form nests metadata; the flat form is a plain map.
		// Either way, anything that isn't a string-to-string map is ignored rather than
		// failing the whole query.
		var meta map[string]string
		if json.Unmarshal(v[2], &meta) == nil {
			for k, val := range meta {
				labels[k] = val
			}
		} else {
			var cat struct {
				StructuredMetadata map[string]string `json:"structuredMetadata"`
				Parsed             map[string]string `json:"parsed"`
			}
			if json.Unmarshal(v[2], &cat) == nil {
				for k, val := range cat.StructuredMetadata {
					labels[k] = val
				}
				for k, val := range cat.Parsed {
					labels[k] = val
				}
			}
		}
	}
	return Entry{Timestamp: ts, Line: line, Labels: labels}, nil
}

// LabelValues returns the values of label seen between start and end, sorted.
func (c *Client) LabelValues(ctx context.Context, label string, start, end time.Time) ([]string, error) {
	q := url.Values{}
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	var body struct {
		Data []string `json:"data"`
	}
	if err := c.get(ctx, "/loki/api/v1/label/"+url.PathEscape(label)+"/values", q, &body); err != nil {
		return nil, err
	}
	values := body.Data
	if values == nil {
		values = []string{}
	}
	sort.Strings(values)
	return values, nil
}

// Ready reports whether Loki says it is ready to serve (GET /ready → 200).
func (c *Client) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/ready", nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("loki: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return &Error{Status: res.StatusCode, Message: strings.TrimSpace(string(msg))}
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("loki: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return &Error{Status: res.StatusCode, Message: strings.TrimSpace(string(msg))}
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("loki: decoding %s response: %w", path, err)
	}
	return nil
}
