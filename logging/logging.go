package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const DefaultFileName = "mod-downloader.log"

type Options struct {
	Disabled bool
	// ForceFile mirrors the config's force_file setting: write to the file and
	// also mirror to stderr when it is usable.
	ForceFile bool
	FilePath  string
	// Level is the minimum level to emit. The zero value is slog.LevelInfo.
	Level slog.Level
	// NoStderr treats stderr as unavailable even when it is a terminal. The TUI
	// uses this so log lines never corrupt the full-screen program; output goes
	// to FilePath only.
	NoStderr bool
}

var (
	loggerMu sync.RWMutex
	logger   = newLogger(os.Stderr, slog.LevelInfo)
	logFile  *os.File
)

func init() {
	slog.SetDefault(logger)
}

func Configure(options Options) error {
	usable := !options.NoStderr && stderrAvailable(os.Stderr)
	return configure(options, os.Stderr, usable)
}

func configure(options Options, stderr io.Writer, stderrUsable bool) error {
	var output io.Writer = io.Discard
	var nextFile *os.File
	if !options.Disabled {
		fileOnly := options.NoStderr
		if options.ForceFile || fileOnly || !stderrUsable {
			path := strings.TrimSpace(options.FilePath)
			if path == "" {
				path = DefaultFileName
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			nextFile = file
			output = file
			if !fileOnly && stderrUsable && stderr != nil {
				output = io.MultiWriter(stderr, file)
			}
		} else if stderr != nil {
			output = stderr
		}
	}

	nextLogger := newLogger(output, options.Level)
	loggerMu.Lock()
	previousFile := logFile
	logger = nextLogger
	logFile = nextFile
	slog.SetDefault(nextLogger)
	if previousFile != nil {
		_ = previousFile.Close()
	}
	loggerMu.Unlock()
	return nil
}

func Close() {
	loggerMu.Lock()
	previousFile := logFile
	logger = newLogger(io.Discard, slog.LevelInfo)
	logFile = nil
	slog.SetDefault(logger)
	if previousFile != nil {
		_ = previousFile.Close()
	}
	loggerMu.Unlock()
}

func newLogger(output io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level}))
}

func stderrAvailable(stderr *os.File) bool {
	if stderr == nil {
		return false
	}
	_, err := stderr.Stat()
	return err == nil
}

func Debug(msg string, args ...any) {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	logger.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	logger.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	logger.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	logger.Error(msg, args...)
}
