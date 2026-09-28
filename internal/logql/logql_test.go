package logql

import (
	"strings"
	"testing"
)

func TestQuery(t *testing.T) {
	cases := []struct {
		name string
		f    Filter
		want string
	}{
		{"everything", Filter{}, `{module=~".+"}`},
		{"one module", Filter{Modules: []string{"storage"}}, `{module=~"storage"}`},
		{"modules sorted and de-duplicated", Filter{Modules: []string{"storage", "catalog", "storage"}}, `{module=~"catalog|storage"}`},
		{"dots in a module are literal", Filter{Modules: []string{"a.b"}}, `{module=~"a\\.b"}`},
		{"search is a case-insensitive literal", Filter{Search: "db down"}, `{module=~".+"} |~ "(?i)db down"`},
		{"regexp metacharacters in search are escaped", Filter{Search: "a.b*(c)"}, `{module=~".+"} |~ "(?i)a\\.b\\*\\(c\\)"`},
		{"quotes and backslashes cannot break out of the literal", Filter{Search: `"} or {x="y` + `\`}, `{module=~".+"} |~ "(?i)\"\\} or \\{x=\"y\\\\"`},
		{"error bucket covers critical and fatal", Filter{Levels: []Level{LevelError}}, `{module=~".+"} | detected_level=~"critical|error|fatal"`},
		{"debug bucket covers trace", Filter{Levels: []Level{LevelDebug, LevelWarn}}, `{module=~".+"} | detected_level=~"debug|trace|warn"`},
		{"all three", Filter{Modules: []string{"catalog"}, Levels: []Level{LevelWarn}, Search: "slow"}, `{module=~"catalog"} |~ "(?i)slow" | detected_level=~"warn"`},
		{"workspace pin is a stream-selector matcher", Filter{Workspace: "acme"}, `{module=~".+", workspace="acme"}`},
		{"workspace pin with every other filter", Filter{Modules: []string{"notebooks"}, Levels: []Level{LevelError}, Search: "oom", Workspace: "acme"},
			`{module=~"notebooks", workspace="acme"} |~ "(?i)oom" | detected_level=~"critical|error|fatal"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.f.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got := tc.f.Query(); got != tc.want {
				t.Errorf("Query()\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	bad := []Filter{
		{Modules: []string{`storage"}`}},
		{Modules: []string{""}},
		{Modules: []string{"-leading-dash"}},
		{Modules: []string{strings.Repeat("a", 64)}},
		{Levels: []Level{"verbose"}},
		{Search: strings.Repeat("x", MaxSearchLength+1)},
		{Search: "two\nlines"},
		{Workspace: "Acme"},
		{Workspace: `acme", workspace=~".*`},
		{Workspace: "a/b"},
	}
	for _, f := range bad {
		if err := f.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want an error", f)
		}
	}
	ok := Filter{Modules: []string{"storage", "kube-proxy", "coredns", "booth_x.1"}, Levels: Levels, Search: strings.Repeat("x", MaxSearchLength)}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate(valid filter) = %v", err)
	}
}

func TestParseLevelAndBucket(t *testing.T) {
	for _, s := range []string{"error", "WARN", " info ", "debug", "unknown"} {
		if _, err := ParseLevel(s); err != nil {
			t.Errorf("ParseLevel(%q): %v", s, err)
		}
	}
	if _, err := ParseLevel("fatal"); err == nil {
		t.Error("ParseLevel(fatal) should fail: fatal is a detected_level, not a bucket")
	}
	for detected, want := range map[string]Level{
		"error": LevelError, "critical": LevelError, "fatal": LevelError, "FATAL": LevelError,
		"warn": LevelWarn, "info": LevelInfo, "trace": LevelDebug, "debug": LevelDebug,
		"unknown": LevelUnknown, "": LevelUnknown, "nonsense": LevelUnknown,
	} {
		if got := BucketOf(detected); got != want {
			t.Errorf("BucketOf(%q) = %q, want %q", detected, got, want)
		}
	}
}
