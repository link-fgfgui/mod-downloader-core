package configs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPreferencesThemeReadsPrefixedEnvironment(t *testing.T) {
	t.Setenv("PREFERS_THEME", "light")
	t.Setenv("PREFERS_ANIMATION_DURATION_MULTIPLIER", "2.5")
	t.Setenv("THEME", "dark")

	var cfg Config
	if err := readEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Prefers.Theme != ThemeLight {
		t.Fatalf("theme = %q, want %q", cfg.Prefers.Theme, ThemeLight)
	}
	if cfg.Prefers.NormalizedAnimationDurationMultiplier() != 2.5 {
		t.Fatalf("animation multiplier = %v, want 2.5", cfg.Prefers.NormalizedAnimationDurationMultiplier())
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
animations_enabled = false
animation_duration_multiplier = 1.75
`, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Prefers.Theme != ThemeSystem {
		t.Fatalf("theme = %q, want %q", cfg.Prefers.Theme, ThemeSystem)
	}
	if cfg.Prefers.AnimationsEnabledValue() {
		t.Fatal("animations enabled = true, want false")
	}
	if cfg.Prefers.NormalizedAnimationDurationMultiplier() != 1.75 {
		t.Fatalf("animation multiplier = %v, want 1.75", cfg.Prefers.NormalizedAnimationDurationMultiplier())
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

func TestPreferencesAnimationDefaultsAndNormalization(t *testing.T) {
	defaults := Preferences{}
	if !defaults.AnimationsEnabledValue() {
		t.Fatal("default animations enabled = false, want true")
	}
	if defaults.NormalizedAnimationDurationMultiplier() != DefaultAnimationDurationMultiplier {
		t.Fatalf("default animation multiplier = %v, want %v", defaults.NormalizedAnimationDurationMultiplier(), DefaultAnimationDurationMultiplier)
	}

	tests := map[float64]float64{
		-1:   DefaultAnimationDurationMultiplier,
		0:    DefaultAnimationDurationMultiplier,
		0.1:  MinAnimationDurationMultiplier,
		0.25: MinAnimationDurationMultiplier,
		1.5:  1.5,
		5:    MaxAnimationDurationMultiplier,
	}
	for value, want := range tests {
		if got := NormalizeAnimationDurationMultiplier(value); got != want {
			t.Fatalf("NormalizeAnimationDurationMultiplier(%v) = %v, want %v", value, got, want)
		}
	}
}
