package configs

import (
	"fmt"
	"math"
	"strings"
)

type APIKeys struct {
	CurseforgeApiKey string `toml:"curseforge_api_key" json:"curseforge_api_key" env:"CF_API_KEY"`
	ModrinthApiKey   string `toml:"modrinth_api_key" json:"modrinth_api_key" env:"MODRINTH_API_KEY"`
}

type Theme string

const (
	ThemeDark   Theme = "dark"
	ThemeLight  Theme = "light"
	ThemeSystem Theme = "system"
)

func (t *Theme) UnmarshalText(text []byte) error {
	theme := ParseTheme(string(text))
	if theme == "" {
		return fmt.Errorf("invalid theme %q: expected dark, light, or system", strings.TrimSpace(string(text)))
	}
	*t = theme
	return nil
}

func (t Theme) MarshalText() ([]byte, error) {
	return []byte(t.Normalized().String()), nil
}

func (t Theme) String() string {
	return string(t)
}

func (t Theme) Normalized() Theme {
	if normalized := ParseTheme(string(t)); normalized != "" {
		return normalized
	}
	return ThemeDark
}

func ParseTheme(value string) Theme {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", string(ThemeDark):
		return ThemeDark
	case "1", string(ThemeLight):
		return ThemeLight
	case "2", string(ThemeSystem):
		return ThemeSystem
	default:
		return ""
	}
}

type Language string

const (
	LanguageSystem  Language = "system"
	LanguageChinese Language = "zh"
	LanguageEnglish Language = "en"
)

func (l *Language) UnmarshalText(text []byte) error {
	language := ParseLanguage(string(text))
	if language == "" {
		return fmt.Errorf("invalid language %q: expected system, zh, or en", strings.TrimSpace(string(text)))
	}
	*l = language
	return nil
}

func (l Language) MarshalText() ([]byte, error) {
	return []byte(l.Normalized().String()), nil
}

func (l Language) String() string {
	return string(l)
}

func (l Language) Normalized() Language {
	if normalized := ParseLanguage(string(l)); normalized != "" {
		return normalized
	}
	return LanguageSystem
}

func ParseLanguage(value string) Language {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(LanguageSystem):
		return LanguageSystem
	case "zh-cn", "zh-hans", "chinese", string(LanguageChinese):
		return LanguageChinese
	case "english", string(LanguageEnglish):
		return LanguageEnglish
	default:
		return ""
	}
}

type AnimationMode string

const (
	AnimationModeOff     AnimationMode = "off"
	AnimationModeVuetify AnimationMode = "vuetify"
	AnimationModeGSAP    AnimationMode = "gsap"
)

func (m *AnimationMode) UnmarshalText(text []byte) error {
	mode := ParseAnimationMode(string(text))
	if mode == "" {
		return fmt.Errorf("invalid animation mode %q: expected off, vuetify, or gsap", strings.TrimSpace(string(text)))
	}
	*m = mode
	return nil
}

func (m AnimationMode) MarshalText() ([]byte, error) {
	return []byte(m.Normalized().String()), nil
}

func (m AnimationMode) String() string {
	return string(m)
}

func (m AnimationMode) Normalized() AnimationMode {
	if normalized := ParseAnimationMode(string(m)); normalized != "" {
		return normalized
	}
	return DefaultAnimationMode
}

func ParseAnimationMode(value string) AnimationMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "true", "on", "enabled", "1", string(AnimationModeVuetify):
		return AnimationModeVuetify
	case "false", "disabled", "0", string(AnimationModeOff):
		return AnimationModeOff
	case "2", string(AnimationModeGSAP):
		return AnimationModeGSAP
	default:
		return ""
	}
}

type Preferences struct {
	Theme                       Theme         `toml:"theme" json:"theme" env:"THEME" env-default:"dark"`
	Language                    Language      `toml:"language" json:"language" env:"LANGUAGE" env-default:"system"`
	MinecraftDir                string        `toml:"minecraft_dir" json:"minecraft_dir" env:"MINECRAFT_DIR"`
	SimpleMode                  bool          `toml:"simple_mode" json:"simple_mode" env:"SIMPLE_MODE"`
	AnimationMode               AnimationMode `toml:"animation_mode" json:"animation_mode" env:"ANIMATION_MODE"`
	AnimationsEnabled           *bool         `toml:"animations_enabled" json:"animations_enabled"`
	AnimationDurationMultiplier float64       `toml:"animation_duration_multiplier" json:"animation_duration_multiplier" env:"ANIMATION_DURATION_MULTIPLIER"`
	AutoScanUnusedDependencies  *bool         `toml:"auto_scan_unused_dependencies" json:"auto_scan_unused_dependencies"`
	MCIMEnabled                 bool          `toml:"mcim_enabled" json:"mcim_enabled" env:"MCIM_ENABLED"`
	PreferredReleaseType        string        `toml:"preferred_release_type" json:"preferred_release_type" env:"PREFERRED_RELEASE_TYPE"`
	EnableJijDetection          *bool         `toml:"enable_jij_detection" json:"enable_jij_detection"`
}

const (
	DefaultAnimationMode               = AnimationModeVuetify
	DefaultAnimationsEnabled           = true
	DefaultAnimationDurationMultiplier = 1.0
	MinAnimationDurationMultiplier     = 0.1
	MaxAnimationDurationMultiplier     = 3.0
	DefaultAutoScanUnusedDependencies  = false
	DefaultPreferredReleaseType        = "release"
	DefaultEnableJijDetection          = true
)

func BoolPtr(value bool) *bool {
	return &value
}

func (p Preferences) AnimationModeValue() AnimationMode {
	if strings.TrimSpace(string(p.AnimationMode)) != "" {
		if mode := ParseAnimationMode(string(p.AnimationMode)); mode != "" {
			return mode
		}
		return DefaultAnimationMode
	}
	if p.AnimationsEnabled == nil {
		return DefaultAnimationMode
	}
	if *p.AnimationsEnabled {
		return AnimationModeVuetify
	}
	return AnimationModeOff
}

func (p Preferences) AnimationsEnabledValue() bool {
	return p.AnimationModeValue() != AnimationModeOff
}

func (p Preferences) NormalizedAnimationDurationMultiplier() float64 {
	return NormalizeAnimationDurationMultiplier(p.AnimationDurationMultiplier)
}

func (p Preferences) AutoScanUnusedDependenciesValue() bool {
	if p.AutoScanUnusedDependencies == nil {
		return DefaultAutoScanUnusedDependencies
	}
	return *p.AutoScanUnusedDependencies
}

func (p Preferences) MCIMEnabledValue() bool {
	return p.MCIMEnabled
}

func (p Preferences) PreferredReleaseTypeValue() string {
	switch strings.ToLower(strings.TrimSpace(p.PreferredReleaseType)) {
	case "alpha", "beta", "release":
		return strings.ToLower(strings.TrimSpace(p.PreferredReleaseType))
	default:
		return DefaultPreferredReleaseType
	}
}

func (p Preferences) EnableJijDetectionValue() bool {
	if p.EnableJijDetection == nil {
		return DefaultEnableJijDetection
	}
	return *p.EnableJijDetection
}

func NormalizeAnimationDurationMultiplier(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return DefaultAnimationDurationMultiplier
	}
	if value < MinAnimationDurationMultiplier {
		return MinAnimationDurationMultiplier
	}
	if value > MaxAnimationDurationMultiplier {
		return MaxAnimationDurationMultiplier
	}
	return value
}

type RuntimeConfig struct {
	MinecraftVersion string `toml:"minecraft_version" json:"minecraftVersion" env:"MINECRAFT_VERSION"`
	ModLoader        string `toml:"mod_loader" json:"modLoader" env:"MOD_LOADER"`
	CacheDir         string `toml:"cache_dir" json:"cacheDir" env:"MOD_DOWNLOADER_CACHE_DIR"`
}

const (
	DefaultFileConcurrency       = 4
	DefaultConcurrentDownloads   = 16
	MinFileConcurrency           = 1
	MaxFileConcurrency           = 32
	MinConcurrentDownloads       = 1
	MaxConcurrentDownloads       = 256
	MinRequestsPerSecond         = 0
	MaxRequestsPerSecond         = 100
	DefaultTargetDownloadRateMiB = 1.0
	MinTargetDownloadRateMiB     = 0.1
	MaxTargetDownloadRateMiB     = 5.0
)

type DownloadConfig struct {
	FileConcurrency         int     `toml:"file_concurrency" json:"fileConcurrency" env:"FILE_CONCURRENCY"`
	ConcurrentDownloads     int     `toml:"concurrent_downloads" json:"concurrentDownloads" env:"CONCURRENT_DOWNLOADS"`
	AdaptiveFileConcurrency bool    `toml:"adaptive_file_concurrency" json:"adaptiveFileConcurrency" env:"ADAPTIVE_FILE_CONCURRENCY"`
	TargetDownloadRateMiB   float64 `toml:"target_download_rate_mib" json:"targetDownloadRateMiB" env:"TARGET_DOWNLOAD_RATE_MIB"`
	VerifySHA1              bool    `toml:"verify_sha1" json:"verifySha1" env:"VERIFY_SHA1"`
}

func (c DownloadConfig) FileConcurrencyValue() int {
	if c.FileConcurrency < MinFileConcurrency {
		return DefaultFileConcurrency
	}
	if c.FileConcurrency > MaxFileConcurrency {
		return MaxFileConcurrency
	}
	return c.FileConcurrency
}

func (c DownloadConfig) ConcurrentDownloadsValue() int {
	if c.ConcurrentDownloads < MinConcurrentDownloads {
		return DefaultConcurrentDownloads
	}
	if c.ConcurrentDownloads > MaxConcurrentDownloads {
		return MaxConcurrentDownloads
	}
	return c.ConcurrentDownloads
}

func (c DownloadConfig) TargetDownloadRateMiBValue() float64 {
	if math.IsNaN(c.TargetDownloadRateMiB) || math.IsInf(c.TargetDownloadRateMiB, 0) || c.TargetDownloadRateMiB <= 0 {
		return DefaultTargetDownloadRateMiB
	}
	if c.TargetDownloadRateMiB < MinTargetDownloadRateMiB {
		return MinTargetDownloadRateMiB
	}
	if c.TargetDownloadRateMiB > MaxTargetDownloadRateMiB {
		return MaxTargetDownloadRateMiB
	}
	return c.TargetDownloadRateMiB
}

type APIConfig struct {
	RequestsPerSecond int `toml:"requests_per_second" json:"requestsPerSecond" env:"REQUESTS_PER_SECOND"`
}

type LoggingConfig struct {
	Enabled   *bool `toml:"enabled,omitempty" json:"enabled,omitempty" env:"-"`
	Disabled  *bool `toml:"disabled,omitempty" json:"-" env:"-"`
	ForceFile bool  `toml:"force_file" json:"forceFile" env:"FORCE_FILE"`
}

func (c LoggingConfig) EnabledValue() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	if c.Disabled != nil {
		return !*c.Disabled
	}
	return true
}

func (c LoggingConfig) Canonical() LoggingConfig {
	return LoggingConfig{Enabled: BoolPtr(c.EnabledValue()), ForceFile: c.ForceFile}
}

func (c APIConfig) RequestsPerSecondValue() int {
	if c.RequestsPerSecond < MinRequestsPerSecond {
		return MinRequestsPerSecond
	}
	if c.RequestsPerSecond > MaxRequestsPerSecond {
		return MaxRequestsPerSecond
	}
	return c.RequestsPerSecond
}

type Config struct {
	Keys      APIKeys        `toml:"keys" json:"keys" env-prefix:"KEYS_"`
	Prefers   Preferences    `toml:"preferences" json:"preferences" env-prefix:"PREFERS_"`
	Runtime   RuntimeConfig  `toml:"runtime" json:"runtime"`
	Downloads DownloadConfig `toml:"downloads" json:"downloads" env-prefix:"DOWNLOADS_"`
	API       APIConfig      `toml:"api" json:"api" env-prefix:"API_"`
	Logging   LoggingConfig  `toml:"logging" json:"logging" env-prefix:"LOGGING_"`
}
