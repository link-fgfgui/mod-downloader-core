package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureDisabledSuppressesAllOutputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	var stderr bytes.Buffer
	if err := configure(Options{Disabled: true, ForceFile: true, FilePath: path}, &stderr, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	Info("hidden message")
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled log file stat error = %v, want not exist", err)
	}
}

func TestConfigureForceFileWritesStderrAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	var stderr bytes.Buffer
	if err := configure(Options{ForceFile: true, FilePath: path}, &stderr, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	Info("forced message", "count", 2)
	if !strings.Contains(stderr.String(), "forced message") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "forced message") || !strings.Contains(string(data), "count=2") {
		t.Fatalf("log file = %q", data)
	}
}

func TestConfigureFallsBackToFileWhenStderrUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	var stderr bytes.Buffer
	if err := configure(Options{FilePath: path}, &stderr, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	Warn("fallback message")
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fallback message") {
		t.Fatalf("log file = %q", data)
	}
}

func TestConfigureUsesOnlyStderrByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	var stderr bytes.Buffer
	if err := configure(Options{FilePath: path}, &stderr, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	Debug("stderr message")
	if !strings.Contains(stderr.String(), "stderr message") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("default log file stat error = %v, want not exist", err)
	}
}

func TestStderrAvailableRejectsClosedFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if stderrAvailable(file) {
		t.Fatal("closed stderr reported available")
	}
}
