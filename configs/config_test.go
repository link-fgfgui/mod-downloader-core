package configs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPreferencesThemeReadsPrefixedEnvironment(t *testing.T) {
	t.Setenv("PREFERS_THEME", "light")
	t.Setenv("THEME", "dark")

	var cfg Config
	if err := readEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Prefers.Theme != ThemeLight {
		t.Fatalf("theme = %q, want %q", cfg.Prefers.Theme, ThemeLight)
	}
}

func TestLoadEnvDoesNotReadTOML(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatal(err)
		}
	})

	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`
[keys]
curseforge_api_key = "from-file"
[preferences]
minecraft_dir = "/from-file"
[runtime]
minecraft_version = "1.20.1"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEYS_CF_API_KEY", "from-env")
	t.Setenv("MINECRAFT_VERSION", "1.21.1")
	t.Setenv("MOD_LOADER", "fabric")
	t.Setenv("MOD_DOWNLOADER_CACHE_DIR", "/tmp/md-cache")

	cfg, err := LoadEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Keys.CurseforgeApiKey != "from-env" {
		t.Fatalf("curseforge key = %q, want from-env", cfg.Keys.CurseforgeApiKey)
	}
	if cfg.Prefers.MinecraftDir != "" {
		t.Fatalf("minecraft dir = %q, want empty", cfg.Prefers.MinecraftDir)
	}
	if cfg.Runtime.MinecraftVersion != "1.21.1" || cfg.Runtime.ModLoader != "fabric" || cfg.Runtime.CacheDir != "/tmp/md-cache" {
		t.Fatalf("runtime = %#v", cfg.Runtime)
	}
}

func TestParseThemeSupportsLegacyNumericValues(t *testing.T) {
	tests := map[string]Theme{
		"0": ThemeDark,
		"1": ThemeLight,
		"2": ThemeSystem,
	}
	for value, want := range tests {
		if got := ParseTheme(value); got != want {
			t.Fatalf("ParseTheme(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestPreferencesThemeReadsStringTOML(t *testing.T) {
	var cfg Config
	if _, err := toml.Decode(`
[preferences]
theme = "system"
`, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Prefers.Theme != ThemeSystem {
		t.Fatalf("theme = %q, want %q", cfg.Prefers.Theme, ThemeSystem)
	}
}

func TestPreferencesThemeReadsLegacyNumericTOML(t *testing.T) {
	var cfg Config
	if _, err := toml.Decode(`
[preferences]
theme = 2
`, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Prefers.Theme != ThemeSystem {
		t.Fatalf("theme = %q, want %q", cfg.Prefers.Theme, ThemeSystem)
	}
}
