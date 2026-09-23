// Package logql turns the viewer's structured filters (modules, severities, a search
// string) into a LogQL query for Loki.
//
// The viewer API deliberately never accepts raw LogQL from a client: every value a user
// types ends up inside a quoted LogQL string literal built here, so a search for
// `"} or {namespace="kube-system"` is a search for that text, not a query rewrite. Raw
// LogQL is what the optional Grafana view is for (ADR 0015), not this API.
package logql

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Label names the collector attaches to every line (charts/booth-logging's collector
// config). The viewer only ever filters on these.
const (
	LabelModule    = "module"
	LabelNamespace = "namespace"
	LabelPod       = "pod"
	LabelContainer = "container"
	LabelStream    = "stream"

	// DetectedLevel is structured metadata Loki itself derives from each line at ingest
	// (limits_config.discover_log_levels): a JSON/logfmt `level` field, or a keyword in a
	// plain-text line. Values: trace, debug, info, warn, error, critical, fatal, unknown.
	DetectedLevel = "detected_level"
)

// Level is one of the severity buckets the viewer offers. Several of Loki's
// detected_level values collapse into one bucket, so a user doesn't have to know Loki's
// vocabulary to find "errors".
type Level string

const (
	LevelError   Level = "error"
	LevelWarn    Level = "warn"
	LevelInfo    Level = "info"
	LevelDebug   Level = "debug"
	LevelUnknown Level = "unknown"
)

// Levels is every bucket, most to least severe.
var Levels = []Level{LevelError, LevelWarn, LevelInfo, LevelDebug, LevelUnknown}

// detectedByLevel maps each bucket to the detected_level values it covers.
var detectedByLevel = map[Level][]string{
	LevelError:   {"error", "critical", "fatal"},
	LevelWarn:    {"warn"},
	LevelInfo:    {"info"},
	LevelDebug:   {"debug", "trace"},
	LevelUnknown: {"unknown"},
}

// ParseLevel validates a bucket name from a request.
func ParseLevel(s string) (Level, error) {
	l := Level(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := detectedByLevel[l]; !ok {
		return "", fmt.Errorf("unknown level %q (want one of error, warn, info, debug, unknown)", s)
	}
	return l, nil
}

// BucketOf maps a detected_level value from Loki back to the bucket shown in the viewer.
// Anything unrecognised (including an absent value, e.g. data ingested before level
// discovery was on) is "unknown".
func BucketOf(detected string) Level {
	d := strings.ToLower(detected)
	for l, vals := range detectedByLevel {
		for _, v := range vals {
			if v == d {
				return l
			}
		}
	}
	return LevelUnknown
}

// moduleRE bounds what a module filter value may look like: Kubernetes label values and
// names are a subset of this, so nothing legitimate is refused. The value is still
// regexp-escaped and quoted below; this is a second, independent guard.
var moduleRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// MaxSearchLength caps the free-text search, keeping Loki's line filter cheap.
const MaxSearchLength = 500

// Filter is what the viewer asks for.
type Filter struct {
	// Modules restricts results to these module label values; empty means every module.
	Modules []string
	// Levels restricts results to these severity buckets; empty means every level.
	Levels []Level
	// Search is a case-insensitive substring every returned line must contain.
	Search string
}

// Validate reports the first problem with f, if any.
func (f Filter) Validate() error {
	for _, m := range f.Modules {
		if !moduleRE.MatchString(m) {
			return fmt.Errorf("invalid module %q", m)
		}
	}
	for _, l := range f.Levels {
		if _, ok := detectedByLevel[l]; !ok {
			return fmt.Errorf("unknown level %q", l)
		}
	}
	if len(f.Search) > MaxSearchLength {
		return fmt.Errorf("search is longer than %d characters", MaxSearchLength)
	}
	if strings.ContainsAny(f.Search, "\n\r") {
		return fmt.Errorf("search must be a single line")
	}
	return nil
}

// Query builds the LogQL for f. Call Validate first; Query assumes a valid filter.
//
// Shape: {module=~"a|b"} |~ "(?i)<escaped search>" | detected_level=~"error|critical|fatal"
//
// Loki requires at least one matcher that can't match the empty string, so "every
// module" is module=~".+" — which also, deliberately, leaves out any stream the collector
// couldn't attribute to a module. The line filter comes before the level filter because
// line filters are the cheapest thing Loki can evaluate.
func (f Filter) Query() string {
	var b strings.Builder

	b.WriteString("{" + LabelModule + "=~")
	if len(f.Modules) == 0 {
		b.WriteString(strconv.Quote(".+"))
	} else {
		b.WriteString(strconv.Quote(alternation(f.Modules)))
	}
	b.WriteString("}")

	if f.Search != "" {
		// LogQL string literals use Go's quoting rules, so strconv.Quote is the exact
		// escaper; QuoteMeta makes the search a literal substring rather than a regexp.
		b.WriteString(" |~ " + strconv.Quote("(?i)"+regexp.QuoteMeta(f.Search)))
	}

	if len(f.Levels) > 0 {
		var detected []string
		for _, l := range f.Levels {
			detected = append(detected, detectedByLevel[l]...)
		}
		b.WriteString(" | " + DetectedLevel + "=~" + strconv.Quote(alternation(detected)))
	}
	return b.String()
}

// alternation builds an anchored-by-Loki regexp alternation of literal values, sorted and
// de-duplicated so the same filter always produces the same query text.
func alternation(values []string) string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, regexp.QuoteMeta(v))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}
