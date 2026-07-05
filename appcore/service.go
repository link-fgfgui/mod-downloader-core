package appcore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	modrinth "codeberg.org/jmansfield/go-modrinth/modrinth"
	"github.com/sjet47/go-curseforge"

	"github.com/link-fgfgui/mod-downloader-core/configs"
	"github.com/link-fgfgui/mod-downloader-core/database"
	"github.com/link-fgfgui/mod-downloader-core/downloader"
	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/logging"
	"github.com/link-fgfgui/mod-downloader-core/minecraft"
	"github.com/link-fgfgui/mod-downloader-core/modbridge"
	"github.com/link-fgfgui/mod-downloader-core/models"
	"github.com/link-fgfgui/mod-downloader-core/providers"
	appstructs "github.com/link-fgfgui/mod-downloader-core/structs"
	structs "github.com/link-fgfgui/mod-downloader-core/structs/minecraft"
)

type EventKind string

const (
	EventSearchModsUpdated      EventKind = "searchModsUpdated"
	EventDownloadStatesUpdated  EventKind = "downloadStatesUpdated"
	EventDownloadQueueUpdated   EventKind = "downloadQueueUpdated"
	EventDownloadFailed         EventKind = "downloadFailed"
	EventMinecraftDirChanged    EventKind = "minecraftDirChanged"
	EventSelectedVersionChanged EventKind = "selectedVersionChanged"
)

const APIKeyKeepSentinel = "<keep>"

type Event struct {
	Kind    EventKind
	Payload any
}

type Options struct {
	Config                *configs.Config
	ConfigOverrides       ConfigOverrides
	Runtime               RuntimeOptions
	LoadMinecraftReleases bool
	OnEvent               func(Event)
}

type RuntimeOptions struct {
	WorkDir          string
	TargetModsDir    string
	MinecraftVersion string
	ModLoader        string
	CacheDir         string
	CachePath        string
	NoConfigFile     bool
}

type ConfigOverrides struct {
	MinecraftDir        string
	CurseForgeAPIKey    string
	ModrinthAPIKey      string
	HasMinecraftDir     bool
	HasCurseForgeAPIKey bool
	HasModrinthAPIKey   bool
}

type Service struct {
	ctx     context.Context
	config  *configs.Config
	options Options
}

type AppPreferences struct {
	Theme string `json:"theme"`
}

type SettingsView struct {
	Theme             string `json:"theme"`
	MinecraftDir      string `json:"minecraftDir"`
	HasCurseforgeKey  bool   `json:"hasCurseforgeKey"`
	CurseforgeKeyMask string `json:"curseforgeKeyMask"`
	HasModrinthKey    bool   `json:"hasModrinthKey"`
	ModrinthKeyMask   string `json:"modrinthKeyMask"`
}

type SaveApiKeysRequest struct {
	CurseforgeApiKey string `json:"curseforgeApiKey"`
	ModrinthApiKey   string `json:"modrinthApiKey"`
}

type InstallWaitResult struct {
	Result appstructs.ModDownloadResult     `json:"result"`
	State  appstructs.DownloadQueueState    `json:"state"`
	Errors []appstructs.DownloadFailedEvent `json:"errors,omitempty"`
}

type InstallToDirRequest struct {
	ProjectID        string            `json:"projectId"`
	Project          models.ModProject `json:"project"`
	TargetDir        string            `json:"targetDir"`
	MinecraftVersion string            `json:"minecraftVersion"`
	ModLoader        string            `json:"modLoader"`
	VersionID        string            `json:"versionId,omitempty"`
}

func New(options Options) *Service {
	return &Service{config: options.Config, options: options}
}

func (s *Service) Startup(ctx context.Context) error {
	s.ctx = ctx
	if s.config == nil {
		cfg, err := s.loadConfig()
		if err != nil {
			logging.Error("load config failed", "error", err)
			cfg = &configs.Config{}
		}
		s.config = cfg
	}
	s.applyConfigOverrides()
	if minecraftDir := minecraft.ExpandPathWithEnv(s.config.Prefers.MinecraftDir); strings.TrimSpace(minecraftDir) != "" {
		s.config.Prefers.MinecraftDir = minecraftDir
		global.SetMinecraftDir(minecraftDir)
	}

	cachePath, err := s.cachePath()
	if err != nil {
		logging.Error("resolve database cache path failed", "error", err)
	} else if err := database.OpenAt(cachePath); err != nil {
		logging.Error("open database failed", "error", err)
	}
	s.configureProviderClients()

	if s.options.LoadMinecraftReleases {
		releaseVersions, err := minecraft.FetchMinecraftReleaseVersions()
		if err != nil {
			logging.Error("fetch minecraft release versions failed", "error", err)
			return err
		}
		global.SetMinecraftReleaseVersions(releaseVersions)
	}
	return nil
}

func (s *Service) Shutdown() {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	s.config.Prefers.MinecraftDir = global.GetMinecraftDir()
	if err := configs.Save(s.config); err != nil {
		logging.Error("save config failed", "error", err)
	}
	database.Close()
}

func (s *Service) Close() {
	database.Close()
}

func (s *Service) Config() *configs.Config {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	return s.config
}

func (s *Service) SearchMods(req appstructs.SearchModsRequest) {
	providers.SearchMods(req, func(update appstructs.SearchModsUpdate) {
		s.emit(EventSearchModsUpdated, update)
	})
}

func (s *Service) SearchModsCollect(req appstructs.SearchModsRequest) appstructs.SearchModsUpdate {
	var last appstructs.SearchModsUpdate
	providers.SearchMods(req, func(update appstructs.SearchModsUpdate) {
		last = update
	})
	return last
}

func (s *Service) ListMatchingProjectVersions(result models.ModProject, minecraftVersion string, modLoader string) []models.ModVersion {
	return providers.ListMatchingProjectVersions(result, minecraftVersion, modLoader)
}

func (s *Service) LookupProject(platform, idOrSlug, mcVersion, modLoader string) (models.ModProject, bool) {
	return providers.LookupProjectByPlatform(platform, idOrSlug, mcVersion, modLoader)
}

func (s *Service) GetPinnedModVersion(platform string, modID string, minecraftVersion string, modLoader string) database.PinnedMod {
	pin, _ := database.GetPinnedMod(platform, modID, minecraftVersion, modLoader)
	return pin
}

func (s *Service) PinModVersion(req appstructs.ModVersionPinRequest) database.PinnedMod {
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	modID := strings.ToLower(strings.TrimSpace(req.ModID))
	versionID := strings.TrimSpace(req.VersionID)
	mcVersion := strings.TrimSpace(req.MinecraftVersion)
	modLoader := strings.ToLower(strings.TrimSpace(req.ModLoader))

	if platform == "" || modID == "" || versionID == "" || mcVersion == "" || modLoader == "" {
		return database.PinnedMod{}
	}

	existing, found := database.GetPinnedMod(platform, modID, mcVersion, modLoader)
	if found && strings.EqualFold(existing.VersionID, versionID) {
		_ = database.DeletePinnedMod(platform, modID, mcVersion, modLoader)
		return database.PinnedMod{}
	}

	pin := database.PinnedMod{
		Platform:         platform,
		ModID:            modID,
		VersionID:        versionID,
		MinecraftVersion: mcVersion,
		ModLoader:        modLoader,
	}
	if err := database.UpsertPinnedMod(pin); err != nil {
		logging.Error("upsert pinned mod failed", "platform", platform, "modID", modID, "versionID", versionID, "minecraftVersion", mcVersion, "modLoader", modLoader, "error", err)
		return database.PinnedMod{}
	}
	pin, _ = database.GetPinnedMod(platform, modID, mcVersion, modLoader)
	return pin
}

func (s *Service) ListPinnedMods() []database.PinnedMod {
	return database.ListPinnedMods()
}

func (s *Service) UnpinMod(platform, modID, mcVersion, modLoader string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	modID = strings.ToLower(strings.TrimSpace(modID))
	mcVersion = strings.TrimSpace(mcVersion)
	modLoader = strings.ToLower(strings.TrimSpace(modLoader))
	if platform == "" || modID == "" || mcVersion == "" || modLoader == "" {
		return false
	}
	if _, found := database.GetPinnedMod(platform, modID, mcVersion, modLoader); !found {
		return false
	}
	if err := database.DeletePinnedMod(platform, modID, mcVersion, modLoader); err != nil {
		logging.Error("unpin mod failed", "platform", platform, "modID", modID, "minecraftVersion", mcVersion, "modLoader", modLoader, "error", err)
		return false
	}
	return true
}

func (s *Service) GetMinecraftReleaseVersions() []string {
	return minecraft.GetMinecraftReleaseVersions()
}

func (s *Service) GetPreferences() AppPreferences {
	if s.config == nil {
		return AppPreferences{Theme: configs.ThemeDark.String()}
	}
	return AppPreferences{Theme: s.config.Prefers.Theme.Normalized().String()}
}

func (s *Service) GetSettings() SettingsView {
	sv := SettingsView{Theme: configs.ThemeDark.String()}
	if s.config != nil {
		sv.Theme = s.config.Prefers.Theme.Normalized().String()
		sv.MinecraftDir = minecraft.SimplifyPathWithEnv(minecraft.ExpandPathWithEnv(s.config.Prefers.MinecraftDir))
		sv.HasCurseforgeKey = strings.TrimSpace(s.config.Keys.CurseforgeApiKey) != ""
		sv.CurseforgeKeyMask = MaskKey(s.config.Keys.CurseforgeApiKey)
		sv.HasModrinthKey = strings.TrimSpace(s.config.Keys.ModrinthApiKey) != ""
		sv.ModrinthKeyMask = MaskKey(s.config.Keys.ModrinthApiKey)
	}
	if sv.MinecraftDir == "" {
		sv.MinecraftDir = minecraft.SimplifyPathWithEnv(global.GetMinecraftDir())
	}
	return sv
}

func (s *Service) SaveTheme(theme string) string {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	parsed := configs.ParseTheme(theme)
	if parsed == "" {
		parsed = configs.ThemeDark
	}
	s.config.Prefers.Theme = parsed
	if err := configs.Save(s.config); err != nil {
		logging.Error("save theme failed", "theme", parsed, "error", err)
	}
	return parsed.String()
}

func (s *Service) SaveMinecraftDirPreference(dir string) string {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	s.config.Prefers.MinecraftDir = strings.TrimSpace(dir)
	if err := configs.Save(s.config); err != nil {
		logging.Error("save minecraft dir preference failed", "minecraftDir", dir, "error", err)
	}
	global.SetMinecraftDir(minecraft.ExpandPathWithEnv(s.config.Prefers.MinecraftDir))
	return s.config.Prefers.MinecraftDir
}

func (s *Service) SaveApiKeys(req SaveApiKeysRequest) SettingsView {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	if req.CurseforgeApiKey != APIKeyKeepSentinel {
		s.config.Keys.CurseforgeApiKey = strings.TrimSpace(req.CurseforgeApiKey)
	}
	if req.ModrinthApiKey != APIKeyKeepSentinel {
		s.config.Keys.ModrinthApiKey = strings.TrimSpace(req.ModrinthApiKey)
	}
	if err := configs.Save(s.config); err != nil {
		logging.Error("save api keys failed", "error", err)
	}
	s.configureProviderClients()
	return s.GetSettings()
}

func (s *Service) QueueModDownload(req appstructs.ModDownloadRequest) appstructs.ModDownloadResult {
	req = s.applyRuntimeDownloadRequest(req)
	return downloader.QueueModDownload(s.ctx, req, s.Config().Keys.CurseforgeApiKey, s.downloadEvents())
}

func (s *Service) InstallModAndWait(ctx context.Context, req appstructs.ModDownloadRequest) InstallWaitResult {
	req = s.applyRuntimeDownloadRequest(req)
	var failed []appstructs.DownloadFailedEvent
	events := s.downloadEvents()
	previousFailed := events.OnDownloadFailed
	events.OnDownloadFailed = func(event appstructs.DownloadFailedEvent) {
		failed = append(failed, event)
		if previousFailed != nil {
			previousFailed(event)
		}
	}

	result := downloader.QueueModDownload(ctx, req, s.Config().Keys.CurseforgeApiKey, events)
	if !result.Queued {
		return InstallWaitResult{Result: result, State: downloader.GetDownloadQueueState()}
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := downloader.GetDownloadQueueState()
		if !state.Active {
			return InstallWaitResult{Result: result, State: state, Errors: failed}
		}
		select {
		case <-ctx.Done():
			return InstallWaitResult{
				Result: appstructs.ModDownloadResult{Skipped: true, Reason: ctx.Err().Error()},
				State:  state,
				Errors: failed,
			}
		case <-ticker.C:
		}
	}
}

func (s *Service) InstallModToDirAndWait(ctx context.Context, req InstallToDirRequest) InstallWaitResult {
	return s.InstallModAndWait(ctx, appstructs.ModDownloadRequest{
		ProjectID:        req.ProjectID,
		Result:           req.Project,
		MinecraftVersion: req.MinecraftVersion,
		ModLoader:        req.ModLoader,
		VersionID:        req.VersionID,
		TargetDir:        req.TargetDir,
	})
}

func (s *Service) GetDownloadQueueState() appstructs.DownloadQueueState {
	return downloader.GetDownloadQueueState()
}

func (s *Service) CancelDownload(id string) bool {
	return downloader.CancelDownload(s.ctx, id, s.downloadEvents())
}

func (s *Service) GetDownloadStates(req appstructs.DownloadStatesRequest) []appstructs.ModDownloadButtonState {
	req = s.applyRuntimeDownloadStatesRequest(req)
	return downloader.GetDownloadStates(req, func() {
		s.emit(EventDownloadStatesUpdated, nil)
	})
}

func (s *Service) SetMinecraftDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	previousDir := global.GetMinecraftDir()
	simplified := minecraft.SimplifyPathWithEnv(dir)
	global.SetMinecraftDir(dir)
	global.ClearLocalMods()
	global.HardlinkIndexClear()
	global.InvalidateVersions()
	versions := s.LoadVersionsFromDisk(dir)
	if len(versions) == 0 {
		logging.Warn("chosen minecraft dir has no versions", "minecraftDir", dir)
		global.SetMinecraftDir(previousDir)
		global.ClearLocalMods()
		global.HardlinkIndexClear()
		global.InvalidateVersions()
		if strings.TrimSpace(previousDir) != "" {
			s.LoadVersionsFromDisk(previousDir)
		}
		return ""
	}
	if s.config != nil {
		s.config.Prefers.MinecraftDir = dir
	}
	s.emit(EventMinecraftDirChanged, simplified)
	s.emit(EventSelectedVersionChanged, global.GetSelectedVersion())
	return simplified
}

func (s *Service) GetMinecraftDir() string {
	return global.GetMinecraftDir()
}

func (s *Service) ValidateMinecraftDir() bool {
	resolvedDir := global.GetMinecraftDir()
	if resolvedDir == "" {
		return false
	}
	info, err := os.Stat(resolvedDir)
	return err == nil && info.IsDir()
}

func (s *Service) GetVersions() []structs.VersionInfo {
	mcDir := global.GetMinecraftDir()
	if versions, ok := global.GetVersionsForDir(mcDir); ok {
		ensureSelectedVersion(versions)
		return versions
	}
	return s.LoadVersionsFromDisk(mcDir)
}

func (s *Service) GetSelectedVersion() structs.VersionInfo {
	return global.GetSelectedVersion()
}

func (s *Service) RefreshVersions() []structs.VersionInfo {
	return s.LoadVersionsFromDisk(global.GetMinecraftDir())
}

func (s *Service) RefreshSelectedVersionMods() structs.VersionInfo {
	selected := global.GetSelectedVersion()
	if selected.ID == "" && selected.Name == "" {
		return structs.VersionInfo{}
	}
	refreshed := s.refreshVersionMods(selected, global.GetMinecraftDir())
	enriched, missedSHA1s := enrichModIcons(refreshed.Mods)
	refreshed.Mods = enriched
	global.SetSelectedVersion(refreshed)
	s.emit(EventSelectedVersionChanged, refreshed)

	if len(missedSHA1s) > 0 {
		go func() {
			resolved := providers.ResolveProjectsByHashes(missedSHA1s)
			if len(resolved) == 0 {
				return
			}
			updated := global.GetSelectedVersion()
			if updated.ID != refreshed.ID && updated.Name != refreshed.Name {
				return
			}
			mods := make([]structs.ModInfo, len(updated.Mods))
			copy(mods, updated.Mods)
			changed := false
			for i := range mods {
				if mods[i].IconURL != "" || strings.TrimSpace(mods[i].SHA1) == "" {
					continue
				}
				sha1 := strings.ToLower(strings.TrimSpace(mods[i].SHA1))
				if project, ok := resolved[sha1]; ok && strings.TrimSpace(project.IconURL) != "" {
					mods[i].IconURL = project.IconURL
					changed = true
				}
			}
			if !changed {
				return
			}
			updated.Mods = mods
			global.SetSelectedVersion(updated)
			s.emit(EventSelectedVersionChanged, updated)
		}()
	}
	return refreshed
}

func (s *Service) SelectVersion(versionKey string) (structs.VersionInfo, error) {
	versionKey = strings.TrimSpace(versionKey)
	if versionKey == "" {
		return structs.VersionInfo{}, errors.New("empty version key")
	}

	mcDir := global.GetMinecraftDir()
	if _, ok := global.GetVersionsForDir(mcDir); !ok {
		s.LoadVersionsFromDisk(mcDir)
	}

	if version, ok := global.GetVersionByKey(versionKey); ok {
		if !ValidMinecraftInstance(version) {
			return structs.VersionInfo{}, errors.New("invalid minecraft version or mod loader")
		}
		version = s.refreshVersionMods(version, mcDir)
		global.SetSelectedVersion(version)
		s.emit(EventSelectedVersionChanged, version)
		return version, nil
	}

	return structs.VersionInfo{}, fmt.Errorf("version not found: %s", versionKey)
}

func (s *Service) LocalMods(versionKey string) ([]structs.ModInfo, error) {
	if strings.TrimSpace(versionKey) != "" {
		if _, err := s.SelectVersion(versionKey); err != nil {
			return nil, err
		}
	}
	selected := s.RefreshSelectedVersionMods()
	return selected.Mods, nil
}

func (s *Service) LocalModsInDir(dir, minecraftVersion, modLoader string) ([]structs.ModInfo, error) {
	dir = s.resolveRuntimePath(dir)
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("empty mods dir")
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("mods path is not a directory: %s", dir)
	}
	minecraftVersion = firstNonEmpty(strings.TrimSpace(minecraftVersion), s.options.Runtime.MinecraftVersion, s.Config().Runtime.MinecraftVersion)
	modLoader = strings.ToLower(firstNonEmpty(strings.TrimSpace(modLoader), s.options.Runtime.ModLoader, s.Config().Runtime.ModLoader))
	instanceID := modbridge.InstanceIDForTargetDir(dir)
	global.ClearLocalModsByInstance(instanceID)
	mods := minecraft.ScanModsDir(dir, instanceID, minecraftVersion, modLoader, "")
	enriched, _ := enrichModIcons(mods)
	return enriched, nil
}

func (s *Service) LoadVersionsFromDisk(mcDir string) []structs.VersionInfo {
	if strings.TrimSpace(mcDir) == "" {
		global.SetVersionsForDir(mcDir, nil)
		return nil
	}

	infos := minecraft.LoadLauncherVersions(mcDir, loadMinecraftDirVersions)
	global.SetVersionsForDir(mcDir, infos)
	ensureSelectedVersion(infos)
	generation := global.HardlinkIndexGeneration()
	go ScanAllModDirsForHardlinkIndex(mcDir, infos, generation)
	return infos
}

func (s *Service) refreshVersionMods(version structs.VersionInfo, mcDir string) structs.VersionInfo {
	versionDirName := versionInstanceDir(version)
	global.ClearLocalModsByInstance(versionDirName)
	refreshed := ScanVersionMods(version, mcDir)
	if versions, ok := global.GetVersionsForDir(mcDir); ok {
		next := make([]structs.VersionInfo, len(versions))
		copy(next, versions)
		for i, cached := range next {
			if cached.Name == version.Name || cached.ID == version.ID {
				next[i] = refreshed
			}
		}
		global.SetVersionsForDir(mcDir, next)
	}
	return refreshed
}

func (s *Service) applyConfigOverrides() {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	if s.options.ConfigOverrides.HasMinecraftDir {
		s.config.Prefers.MinecraftDir = s.options.ConfigOverrides.MinecraftDir
	}
	if s.options.ConfigOverrides.HasCurseForgeAPIKey {
		s.config.Keys.CurseforgeApiKey = s.options.ConfigOverrides.CurseForgeAPIKey
	}
	if s.options.ConfigOverrides.HasModrinthAPIKey {
		s.config.Keys.ModrinthApiKey = s.options.ConfigOverrides.ModrinthAPIKey
	}
}

func (s *Service) loadConfig() (*configs.Config, error) {
	if s.options.Runtime.NoConfigFile {
		return configs.LoadEnv()
	}
	return configs.Load()
}

func (s *Service) cachePath() (string, error) {
	if path := s.resolveRuntimePath(s.options.Runtime.CachePath); path != "" {
		return path, nil
	}
	cacheDir := firstNonEmpty(s.options.Runtime.CacheDir, s.Config().Runtime.CacheDir)
	if cacheDir != "" {
		return filepath.Join(s.resolveRuntimePath(cacheDir), database.CacheFileName), nil
	}
	return database.DefaultCachePath()
}

func (s *Service) applyRuntimeDownloadRequest(req appstructs.ModDownloadRequest) appstructs.ModDownloadRequest {
	if strings.TrimSpace(req.TargetDir) == "" {
		req.TargetDir = s.resolveRuntimePath(s.options.Runtime.TargetModsDir)
	} else {
		req.TargetDir = s.resolveRuntimePath(req.TargetDir)
	}
	if strings.TrimSpace(req.MinecraftVersion) == "" {
		req.MinecraftVersion = firstNonEmpty(s.options.Runtime.MinecraftVersion, s.Config().Runtime.MinecraftVersion)
	}
	if strings.TrimSpace(req.ModLoader) == "" {
		req.ModLoader = firstNonEmpty(s.options.Runtime.ModLoader, s.Config().Runtime.ModLoader)
	}
	return req
}

func (s *Service) applyRuntimeDownloadStatesRequest(req appstructs.DownloadStatesRequest) appstructs.DownloadStatesRequest {
	if strings.TrimSpace(req.TargetDir) == "" {
		req.TargetDir = s.resolveRuntimePath(s.options.Runtime.TargetModsDir)
	} else {
		req.TargetDir = s.resolveRuntimePath(req.TargetDir)
	}
	if strings.TrimSpace(req.MinecraftVersion) == "" {
		req.MinecraftVersion = firstNonEmpty(s.options.Runtime.MinecraftVersion, s.Config().Runtime.MinecraftVersion)
	}
	if strings.TrimSpace(req.ModLoader) == "" {
		req.ModLoader = firstNonEmpty(s.options.Runtime.ModLoader, s.Config().Runtime.ModLoader)
	}
	return req
}

func (s *Service) resolveRuntimePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = minecraft.ExpandPathWithEnv(path)
	if filepath.IsAbs(path) || strings.TrimSpace(s.options.Runtime.WorkDir) == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(s.options.Runtime.WorkDir, path))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Service) configureProviderClients() {
	if strings.TrimSpace(s.Config().Keys.CurseforgeApiKey) != "" {
		global.SetCurseForgeClient(curseforge.NewClient(s.config.Keys.CurseforgeApiKey))
	} else {
		global.SetCurseForgeClient(nil)
	}
	modrinthClient := modrinth.NewClient(&http.Client{Timeout: 10 * time.Second})
	modrinthClient.UserAgent = "mod-downloader"
	global.SetModrinthClient(modrinthClient)
}

func (s *Service) downloadEvents() downloader.Events {
	return downloader.Events{
		OnQueueState: func(state appstructs.DownloadQueueState) {
			s.emit(EventDownloadQueueUpdated, state)
		},
		OnDownloadFailed: func(event appstructs.DownloadFailedEvent) {
			s.emit(EventDownloadFailed, event)
		},
	}
}

func (s *Service) emit(kind EventKind, payload any) {
	if s.options.OnEvent != nil {
		s.options.OnEvent(Event{Kind: kind, Payload: payload})
	}
}

func MaskKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "****" + value[len(value)-4:]
}

func loadMinecraftDirVersions(gameDir string) []structs.VersionInfo {
	versionsDir := filepath.Join(gameDir, "versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		logging.Error("read versions dir failed", "versionsDir", versionsDir, "error", err)
		return nil
	}

	infos := make([]structs.VersionInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		versionID := entry.Name()
		versionDir := filepath.Join(versionsDir, versionID)
		jsonPath := filepath.Join(versionDir, versionID+".json")
		info, ok := minecraft.CheckManifest(jsonPath)
		if !ok {
			continue
		}
		if !ValidMinecraftInstance(info) {
			logging.Warn("skip invalid minecraft version", "versionID", versionID, "minecraftVersion", info.MinecraftVersion, "modLoader", info.ModLoader)
			continue
		}
		infos = append(infos, info)
	}
	return infos
}

func ensureSelectedVersion(versions []structs.VersionInfo) {
	if len(versions) == 0 {
		return
	}
	selected := global.GetSelectedVersion()
	if selected.ID != "" || selected.Name != "" {
		return
	}
	global.SetSelectedVersion(versions[0])
}

func FindVersionByKey(versions []structs.VersionInfo, key string) (structs.VersionInfo, bool) {
	for _, version := range versions {
		if version.Name == key || version.ID == key {
			return version, true
		}
	}
	return structs.VersionInfo{}, false
}

func versionInstanceDir(version structs.VersionInfo) string {
	if strings.TrimSpace(version.ID) != "" {
		return strings.TrimSpace(version.ID)
	}
	return strings.TrimSpace(version.Name)
}

func ValidMinecraftInstance(version structs.VersionInfo) bool {
	switch strings.ToLower(strings.TrimSpace(version.ModLoader)) {
	case "fabric", "forge", "neoforge":
		return strings.TrimSpace(version.MinecraftVersion) != ""
	default:
		return false
	}
}

func ScanVersionMods(version structs.VersionInfo, mcDir string) structs.VersionInfo {
	versionDirName := versionInstanceDir(version)
	if strings.TrimSpace(mcDir) == "" || strings.TrimSpace(versionDirName) == "" {
		version.Mods = nil
		return version
	}
	versionDir := minecraft.VersionDirPath(mcDir, version)
	version.Mods = minecraft.ScanVersionMods(versionDir, versionDirName, version.MinecraftVersion, version.ModLoader, mcDir)
	return version
}

func enrichModIcons(mods []structs.ModInfo) ([]structs.ModInfo, []string) {
	enriched := make([]structs.ModInfo, len(mods))
	copy(enriched, mods)
	var missedSHA1s []string
	for i := range enriched {
		sha1 := strings.ToLower(strings.TrimSpace(enriched[i].SHA1))
		if sha1 == "" {
			continue
		}
		project, _, ok := modbridge.PlatformMetadataForSHA1(sha1)
		if ok && strings.TrimSpace(project.IconURL) != "" {
			enriched[i].IconURL = project.IconURL
		} else {
			missedSHA1s = append(missedSHA1s, sha1)
		}
	}
	return enriched, missedSHA1s
}

func ScanAllModDirsForHardlinkIndex(mcDir string, versions []structs.VersionInfo, generation uint64) {
	if global.GetMinecraftDir() != mcDir || global.HardlinkIndexGeneration() != generation {
		return
	}
	minecraft.ScanAllModDirsForHardlink(mcDir, versions, func(sha1, path string) bool {
		if global.GetMinecraftDir() != mcDir {
			return false
		}
		return global.HardlinkIndexAddForGeneration(sha1, path, generation)
	})
}
