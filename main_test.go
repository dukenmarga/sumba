package main

import (
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseCookie(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []http.Cookie
	}{
		{
			name:  "multiple cookies with spaces",
			input: "foo=bar; baz = qux ; ignored ; empty=",
			expected: []http.Cookie{
				{Name: "foo", Value: "bar"},
				{Name: "baz", Value: "qux"},
				{Name: "empty", Value: ""},
			},
		},
		{
			name:     "no cookies",
			input:    "",
			expected: []http.Cookie{},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := parseCookie(tc.input)
			if len(result) != len(tc.expected) {
				t.Fatalf("expected %d cookies, got %d", len(tc.expected), len(result))
			}
			for i, cookie := range result {
				name := strings.TrimSpace(cookie.Name)
				value := strings.TrimSpace(cookie.Value)
				if name != tc.expected[i].Name || value != tc.expected[i].Value {
					t.Errorf("cookie %d mismatch: expected (%q,%q), got (%q,%q)",
						i, tc.expected[i].Name, tc.expected[i].Value, name, value)
				}
			}
		})
	}
}

func TestFormatDurationUnit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          float64
		expectedValue  float64
		expectedSuffix string
	}{
		{name: "microseconds range", input: 500, expectedValue: 500, expectedSuffix: "μs"},
		{name: "milliseconds range", input: 20_000, expectedValue: 20, expectedSuffix: "ms"},
		{name: "seconds range", input: 3_000_000, expectedValue: 3, expectedSuffix: "s"},
		{name: "boundary micro", input: 1_000, expectedValue: 1_000, expectedSuffix: "μs"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value, suffix := formatDurationUnit(tc.input)
			if value != tc.expectedValue || suffix != tc.expectedSuffix {
				t.Fatalf("formatDurationUnit(%v) = (%v, %q), want (%v, %q)",
					tc.input, value, suffix, tc.expectedValue, tc.expectedSuffix)
			}
		})
	}
}

func TestReadFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "payload.txt")
	content := []byte("hello world")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	data, err := readFile(path)
	if err != nil {
		t.Fatalf("readFile returned error: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("unexpected data: got %q want %q", string(data), string(content))
	}

	if _, err := readFile(""); err == nil {
		t.Fatal("expected error for empty file path, got nil")
	}
}

func TestNewCounterChannel(t *testing.T) {
	t.Parallel()

	counter := NewCounterChannel(5)
	const operations = 1000

	var wg sync.WaitGroup
	wg.Add(operations)
	for i := 0; i < operations; i++ {
		go func() {
			counter.Add(1)
			wg.Done()
		}()
	}
	wg.Wait()

	got := counter.Read()
	want := uint64(operations + 5)
	if got != want {
		t.Fatalf("counter.Read() = %d, want %d", got, want)
	}
}

func TestRequestTrackerAggregations(t *testing.T) {
	t.Parallel()

	reqs := []RequestTracker{
		{connectTime: 100, totalTime: 1_000_000},
		{connectTime: 300, totalTime: 1_000_000},
	}
	ptr := &reqs

	if got := SumConnectTime(ptr); got != 400 {
		t.Fatalf("SumConnectTime() = %v, want 400", got)
	}

	if got := SumTotalTime(ptr); got != 2_000_000 {
		t.Fatalf("SumTotalTime() = %v, want 2000000", got)
	}

	if got := AverageConnectTime(ptr); got != 200 {
		t.Fatalf("AverageConnectTime() = %v, want 200", got)
	}

	if got := AverageTotalTime(ptr); got != 1_000_000 {
		t.Fatalf("AverageTotalTime() = %v, want 1000000", got)
	}

	if got := RequestPerSecond(ptr, 20); math.Abs(got-10) > 1e-9 {
		t.Fatalf("RequestPerSecond() = %v, want 10", got)
	}
}
