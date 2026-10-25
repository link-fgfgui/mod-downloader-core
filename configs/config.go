package configs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/link-fgfgui/mod-downloader-core/logging"

	"github.com/BurntSushi/toml"
	"github.com/ilyakaznacheev/cleanenv"
)

const configFileName = "mod-downloader.toml"

type loggingOnlyConfig struct {
	Logging LoggingConfig `toml:"logging" env-prefix:"LOGGING_"`
}

// AppConfigPath returns the path to the config file in pwd.
func AppConfigPath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

// Load reads configuration from config.toml and environment variables.
// Environment variables override file values.
func Load() (*Config, error) {
	var cfg Config

	path, err := AppConfigPath()
	if err != nil {
		logging.Error("resolve config path failed", "error", err)
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	logging.Debug("load config started", "path", path)

	if err := cleanenv.ReadConfig(path, &cfg); err != nil {
		if os.IsNotExist(err) {
			logging.Info("config file not found, loading from environment", "path", path)
			if err := readEnv(&cfg); err != nil {
				logging.Error("load config from environment failed", "error", err)
				return nil, fmt.Errorf("read env: %w", err)
			}
			cfg.Logging = cfg.Logging.Canonical()
			logging.Info("config loaded from environment", "hasCurseforgeKey", cfg.Keys.CurseforgeApiKey != "", "hasModrinthKey", cfg.Keys.ModrinthApiKey != "", "minecraftDir", cfg.Prefers.MinecraftDir)
			return &cfg, nil
		}
		logging.Error("load config file failed", "path", path, "error", err)
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := readLoggingEnv(&cfg.Logging); err != nil {
		return nil, fmt.Errorf("read logging env: %w", err)
	}

	cfg.Logging = cfg.Logging.Canonical()
	logging.Info("config loaded from file", "path", path, "hasCurseforgeKey", cfg.Keys.CurseforgeApiKey != "", "hasModrinthKey", cfg.Keys.ModrinthApiKey != "", "minecraftDir", cfg.Prefers.MinecraftDir)
	return &cfg, nil
}

// LoadLogging reads only the logging bootstrap settings without emitting logs.
// App startup uses it before the regular config loader so disabled logging is
// honored from the first config-loader message onward.
func LoadLogging() (LoggingConfig, error) {
	var cfg loggingOnlyConfig
	path, err := AppConfigPath()
	if err != nil {
		return LoggingConfig{}, err
	}
	if err := cleanenv.ReadConfig(path, &cfg); err != nil {
		if os.IsNotExist(err) {
			if err := cleanenv.ReadEnv(&cfg); err != nil {
				return LoggingConfig{}, err
			}
			if err := readLoggingEnv(&cfg.Logging); err != nil {
				return LoggingConfig{}, err
			}
			return cfg.Logging.Canonical(), nil
		}
		return LoggingConfig{}, err
	}
	if err := readLoggingEnv(&cfg.Logging); err != nil {
		return LoggingConfig{}, err
	}
	return cfg.Logging.Canonical(), nil
}

func readEnv(cfg *Config) error {
	if err := cleanenv.ReadEnv(cfg); err != nil {
		return err
	}
	return readLoggingEnv(&cfg.Logging)
}

func readLoggingEnv(cfg *LoggingConfig) error {
	if raw, ok := os.LookupEnv("LOGGING_ENABLED"); ok {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("parse LOGGING_ENABLED: %w", err)
		}
		cfg.Enabled = BoolPtr(value)
	}
	if raw, ok := os.LookupEnv("LOGGING_DISABLED"); ok {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("parse LOGGING_DISABLED: %w", err)
		}
		cfg.Disabled = BoolPtr(value)
	}
	return nil
}

// LoadEnv reads configuration from environment variables only.
func LoadEnv() (*Config, error) {
	var cfg Config
	if err := readEnv(&cfg); err != nil {
		logging.Error("load config from environment failed", "error", err)
		return nil, fmt.Errorf("read env: %w", err)
	}
	cfg.Logging = cfg.Logging.Canonical()
	logging.Info("config loaded from environment", "hasCurseforgeKey", cfg.Keys.CurseforgeApiKey != "", "hasModrinthKey", cfg.Keys.ModrinthApiKey != "", "minecraftDir", cfg.Prefers.MinecraftDir, "cacheDir", cfg.Runtime.CacheDir)
	return &cfg, nil
}

// Save writes the configuration to mod-downloader.toml in pwd.
func Save(cfg *Config) error {
	path, err := AppConfigPath()
	if err != nil {
		logging.Error("resolve config path failed", "error", err)
		return fmt.Errorf("resolve config path: %w", err)
	}
	logging.Debug("save config started", "path", path)

	f, err := os.Create(path)
	if err != nil {
		logging.Error("create config file failed", "path", path, "error", err)
		return fmt.Errorf("create config %s: %w", path, err)
	}
	defer f.Close()

	canonical := *cfg
	canonical.Logging = cfg.Logging.Canonical()
	encoder := toml.NewEncoder(f)
	if err := encoder.Encode(&canonical); err != nil {
		logging.Error("encode config failed", "path", path, "error", err)
		return fmt.Errorf("encode config: %w", err)
	}

	logging.Debug("config saved", "path", path, "hasCurseforgeKey", cfg.Keys.CurseforgeApiKey != "", "hasModrinthKey", cfg.Keys.ModrinthApiKey != "", "minecraftDir", cfg.Prefers.MinecraftDir)
	return nil
}
