// Package lokitest connects tests to a real Loki (hack/docker-compose.loki.yml) and pushes
// fixture lines into it the way the collector would, so the query path is tested against
// the real thing rather than a mock of Loki's response format.
//
// BOOTH_TEST_LOKI_URL names the Loki; without it, tests using URL skip — unless
// BOOTH_TEST_STRICT=1 (set in CI), which turns a missing Loki (or, in test/contract, a
// missing helm) into a failure, so the suite can never pass green by skipping everything
// that matters.
package lokitest

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// URL returns the test Loki's base URL once it reports ready, or skips/fails the test.
func URL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("BOOTH_TEST_LOKI_URL")
	if u == "" {
		if os.Getenv("BOOTH_TEST_STRICT") == "1" {
			t.Fatal("BOOTH_TEST_STRICT=1 but BOOTH_TEST_LOKI_URL is not set")
		}
		t.Skip("BOOTH_TEST_LOKI_URL not set; start hack/docker-compose.loki.yml to run the real-Loki tests")
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		res, err := http.Get(u + "/ready")
		if err == nil {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return u
			}
			err = &readyError{string(body)}
		}
		if time.Now().After(deadline) {
			t.Fatalf("loki at %s never became ready: %v", u, err)
		}
		time.Sleep(time.Second)
	}
}

type readyError struct{ body string }

func (e *readyError) Error() string { return "not ready: " + e.body }

// UniqueModule returns a module label value no other test run uses, so tests sharing one
// long-lived Loki never see each other's lines.
func UniqueModule(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "it-" + hex.EncodeToString(b)
}

// Line is one fixture log line.
type Line struct {
	At   time.Time
	Text string
}

// Push writes lines under one stream with the given labels via Loki's push API.
func Push(t *testing.T, baseURL string, labels map[string]string, lines ...Line) {
	t.Helper()
	values := make([][2]string, len(lines))
	for i, l := range lines {
		values[i] = [2]string{strconv.FormatInt(l.At.UnixNano(), 10), l.Text}
	}
	body, _ := json.Marshal(map[string]any{"streams": []any{map[string]any{"stream": labels, "values": values}}})
	res, err := http.Post(baseURL+"/loki/api/v1/push", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(res.Body)
		t.Fatalf("push: HTTP %d: %s", res.StatusCode, msg)
	}
}

// Eventually retries check until it returns true or the timeout passes. Loki serves pushed
// lines from the ingester almost immediately, but not synchronously with the push.
func Eventually(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met before timeout")
		}
		time.Sleep(250 * time.Millisecond)
	}
}
