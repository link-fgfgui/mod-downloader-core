package filetransfer

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStdlibBackendDownloadsRangesToMemory(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 8)
	server, ranges := newRangeServer(t, data, "X-Test", "memory")
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "nested", "file.jar")
	var callbacks []Progress
	var callbackMu sync.Mutex
	tracker := NewProgressTracker(func(progress Progress) {
		callbackMu.Lock()
		callbacks = append(callbacks, progress)
		callbackMu.Unlock()
	})
	result, err := NewStdlibBackend(server.Client()).Download(nil, Request{
		URL:         server.URL,
		Destination: destination,
		Headers:     map[string]string{"X-Test": "memory"},
		Concurrency: 3,
		ChunkSize:   17,
		MemoryLimit: int64(len(data)),
	}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	assertDownloadedFile(t, destination, data)
	if !result.RangeSupported || result.BytesComplete != int64(len(data)) || result.TotalBytes != int64(len(data)) {
		t.Fatalf("result = %#v", result)
	}
	if snapshot := tracker.Snapshot(); snapshot.BytesComplete != int64(len(data)) || snapshot.TotalBytes != int64(len(data)) {
		t.Fatalf("progress = %#v", snapshot)
	}
	callbackMu.Lock()
	callbackCount := len(callbacks)
	callbackMu.Unlock()
	if callbackCount == 0 {
		t.Fatal("progress callback was not called")
	}
	if got := rangesSnapshot(ranges); len(got) < 2 {
		t.Fatalf("range requests = %#v", got)
	}
}

func TestStdlibBackendAddsRangeWorkersWhenAdaptiveTargetIsMissed(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 32)
	var active atomic.Int64
	var peak atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start, end, ok := parseTestRange(req.Header.Get("Range"), int64(len(data)))
		if !ok {
			http.Error(w, "range required", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
	defer server.Close()

	previousInterval := adaptiveRampInterval
	adaptiveRampInterval = 5 * time.Millisecond
	t.Cleanup(func() { adaptiveRampInterval = previousInterval })
	destination := filepath.Join(t.TempDir(), "adaptive.jar")
	_, err := NewStdlibBackend(server.Client()).Download(t.Context(), Request{
		URL:                  server.URL,
		Destination:          destination,
		Concurrency:          1,
		ChunkSize:            32,
		MemoryLimit:          int64(len(data)),
		AdaptiveConcurrency:  true,
		TargetBytesPerSecond: 1 << 30,
	}, NewProgressTracker(nil))
	if err != nil {
		t.Fatal(err)
	}
	assertDownloadedFile(t, destination, data)
	if peak.Load() < 2 {
		t.Fatalf("adaptive peak range workers = %d, want at least 2", peak.Load())
	}
}

func TestStdlibBackendResumesLargeRangePartsInMD5TempDir(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefgh"), 16)
	server, ranges := newRangeServer(t, data, "", "")
	defer server.Close()

	root := t.TempDir()
	request := Request{
		URL:         server.URL,
		Destination: filepath.Join(root, "file.jar"),
		TempDir:     filepath.Join(root, "tmp"),
		Concurrency: 2,
		ChunkSize:   32,
		MemoryLimit: 16,
	}
	tempDir := rangedTempDir(request)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "000000.part"), data[:7], 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewStdlibBackend(server.Client()).Download(t.Context(), request, NewProgressTracker(nil))
	if err != nil {
		t.Fatal(err)
	}
	assertDownloadedFile(t, request.Destination, data)
	if result.BytesComplete != int64(len(data)) {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("temporary directory still exists: %v", err)
	}
	if !containsString(rangesSnapshot(ranges), "bytes=7-31") {
		t.Fatalf("range requests = %#v, want resumed first part", rangesSnapshot(ranges))
	}
}

func TestStdlibBackendFallsBackToDirectDownloadWithoutRanges(t *testing.T) {
	data := []byte("direct-download")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if req.Header.Get("X-Test") != "direct" {
			t.Errorf("X-Test = %q", req.Header.Get("X-Test"))
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "file.jar")
	result, err := NewStdlibBackend(server.Client()).Download(t.Context(), Request{
		URL:         server.URL,
		Destination: destination,
		Headers:     map[string]string{"X-Test": "direct"},
	}, NewProgressTracker(nil))
	if err != nil {
		t.Fatal(err)
	}
	assertDownloadedFile(t, destination, data)
	if result.RangeSupported || result.BytesComplete != int64(len(data)) {
		t.Fatalf("result = %#v", result)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want probe + direct", requests)
	}
}

func newRangeServer(t *testing.T, data []byte, requiredHeader, requiredValue string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	ranges := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if requiredHeader != "" && req.Header.Get(requiredHeader) != requiredValue {
			t.Errorf("%s = %q", requiredHeader, req.Header.Get(requiredHeader))
		}
		rangeHeader := req.Header.Get("Range")
		mu.Lock()
		ranges = append(ranges, rangeHeader)
		mu.Unlock()
		if rangeHeader == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		start, end, ok := parseTestRange(rangeHeader, int64(len(data)))
		if !ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
	return server, &ranges
}

func parseTestRange(value string, size int64) (int64, int64, bool) {
	value = strings.TrimPrefix(value, "bytes=")
	startText, endText, ok := strings.Cut(value, "-")
	if !ok {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end, err := strconv.ParseInt(endText, 10, 64)
	if err != nil || start < 0 || start > end || end >= size {
		return 0, 0, false
	}
	return start, end, true
}

func rangesSnapshot(ranges *[]string) []string {
	return append([]string(nil), (*ranges)...)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertDownloadedFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("downloaded bytes = %d, want %d", len(got), len(want))
	}
}
