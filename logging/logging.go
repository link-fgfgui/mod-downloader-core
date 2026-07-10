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
	Disabled  bool
	ForceFile bool
	FilePath  string
}

var (
	loggerMu sync.RWMutex
	logger   = newLogger(os.Stderr)
	logFile  *os.File
)

func init() {
	slog.SetDefault(logger)
}

func Configure(options Options) error {
	return configure(options, os.Stderr, stderrAvailable(os.Stderr))
}

func configure(options Options, stderr io.Writer, stderrUsable bool) error {
	var output io.Writer = io.Discard
	var nextFile *os.File
	if !options.Disabled {
		if options.ForceFile || !stderrUsable {
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
			if stderrUsable && stderr != nil {
				output = io.MultiWriter(stderr, file)
			}
		} else if stderr != nil {
			output = stderr
		}
	}

	nextLogger := newLogger(output)
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
	logger = newLogger(io.Discard)
	logFile = nil
	slog.SetDefault(logger)
	if previousFile != nil {
		_ = previousFile.Close()
	}
	loggerMu.Unlock()
}

func newLogger(output io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug}))
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
