package appcore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	modrinth "codeberg.org/jmansfield/go-modrinth/modrinth"
	"github.com/sjet47/go-curseforge"
	curseforgeapi "github.com/sjet47/go-curseforge/api"

	"github.com/link-fgfgui/mod-downloader-core/configs"
	"github.com/link-fgfgui/mod-downloader-core/downloader"
	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/logging"
	"github.com/link-fgfgui/mod-downloader-core/minecraft"
	"github.com/link-fgfgui/mod-downloader-core/modbridge"
	"github.com/link-fgfgui/mod-downloader-core/models"
	"github.com/link-fgfgui/mod-downloader-core/networking"
	"github.com/link-fgfgui/mod-downloader-core/providers"
	"github.com/link-fgfgui/mod-downloader-core/storage"
	appstructs "github.com/link-fgfgui/mod-downloader-core/structs"
	structs "github.com/link-fgfgui/mod-downloader-core/structs/minecraft"
)

const minecraftReleaseCacheTTL = 24 * time.Hour

var (
	fetchMinecraftReleaseVersions   = minecraft.FetchMinecraftReleaseVersions
	configureRemoteModIDConcurrency = modbridge.ConfigureRemoteModIDConcurrency
	configureSimpleMode             = downloader.ConfigureSimpleMode
)

const APIKeyKeepSentinel = "<keep>"

var (
	favoriteCrossVersionCopyLookupProjectByID   = providers.LookupProjectByPlatformID
	favoriteCrossVersionCopyLookupProjectBySlug = providers.LookupProjectByPlatformSlug
)

const (
	LocalModBatchActionEnable  = "enable"
	LocalModBatchActionDisable = "disable"
	LocalModBatchActionInvert  = "invert"
	LocalModBatchActionDelete  = "delete"
)

func New(options Options) *Service {
	return &Service{config: options.Config, options: options}
}

func (s *Service) UserAgent() string {
	return userAgentForVersion(s.options.Version)
}

func (s *Service) Startup(ctx context.Context) error {
	s.ctx = ctx
	providers.StartBackgroundTasks()
	if s.config != nil {
		s.configureLoggingConfig(s.config.Logging)
	} else if bootstrapLogging, err := configs.LoadLogging(); err == nil {
		s.configureLoggingConfig(bootstrapLogging)
	}
	if s.config == nil {
		cfg, err := s.loadConfig()
		if err != nil {
			logging.Error("load config failed", "error", err)
			cfg = &configs.Config{}
		}
		s.config = cfg
	}
	s.applyConfigOverrides()
	s.configureLogging()
	downloader.Configure(downloader.Config{
		FileConcurrency:         s.config.Downloads.FileConcurrencyValue(),
		ConcurrentDownloads:     s.config.Downloads.ConcurrentDownloadsValue(),
		AdaptiveFileConcurrency: s.config.Downloads.AdaptiveFileConcurrency,
		TargetDownloadRateMiB:   s.config.Downloads.TargetDownloadRateMiBValue(),
		VerifySHA1:              s.config.Downloads.VerifySHA1,
	})
	configureSimpleMode(s.config.Prefers.SimpleMode)
	configureRemoteModIDConcurrency(s.config.Downloads.ConcurrentDownloadsValue())
	if minecraftDir := minecraft.ExpandPathWithEnv(s.config.Prefers.MinecraftDir); strings.TrimSpace(minecraftDir) != "" {
		s.config.Prefers.MinecraftDir = minecraftDir
		global.SetMinecraftDir(minecraftDir)
	}

	cachePath, err := s.cachePath()
	if err != nil {
		logging.Error("resolve database cache path failed", "error", err)
	} else if err := storage.OpenAt(cachePath); err != nil {
		logging.Error("open database failed", "error", err)
	}
	s.configureProviderClients()

	if s.options.LoadMinecraftReleases {
		if err := loadMinecraftReleaseVersions(); err != nil {
			return err
		}
	}
	return nil
}

func loadMinecraftReleaseVersions() error {
	cached, updatedAt := storage.GetMinecraftReleaseVersions()
	if len(cached) > 0 && updatedAt >= time.Now().Add(-minecraftReleaseCacheTTL).Unix() {
		global.SetMinecraftReleaseVersions(cached)
		return nil
	}

	releaseVersions, err := fetchMinecraftReleaseVersions()
	if err != nil {
		if len(cached) > 0 {
			logging.Warn("refresh minecraft release versions failed, using stale cache", "versionCount", len(cached), "updatedAt", updatedAt, "error", err)
			global.SetMinecraftReleaseVersions(cached)
			return nil
		}
		logging.Error("fetch minecraft release versions failed", "error", err)
		return err
	}
	global.SetMinecraftReleaseVersions(releaseVersions)
	if err := storage.SetMinecraftReleaseVersions(releaseVersions, time.Now().Unix()); err != nil {
		logging.Warn("cache minecraft release versions failed", "versionCount", len(releaseVersions), "error", err)
	}
	return nil
}

func (s *Service) Shutdown() {
	s.stopModsWatcher()
	if s.config == nil {
		s.config = &configs.Config{}
	}
	s.config.Prefers.MinecraftDir = global.GetMinecraftDir()
	if err := configs.Save(s.config); err != nil {
		logging.Error("save config failed", "error", err)
	}
	providers.StopBackgroundTasks()
	storage.Close()
	logging.Close()
}

func (s *Service) Close() {
	s.stopModsWatcher()
	providers.StopBackgroundTasks()
	storage.Close()
	logging.Close()
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

func (s *Service) SearchModsTextCollect(req appstructs.SearchModsRequest) appstructs.SearchModsUpdate {
	var last appstructs.SearchModsUpdate
	providers.SearchModsText(req, func(update appstructs.SearchModsUpdate) {
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

func (s *Service) LookupProjectBySlug(platform, slug, mcVersion, modLoader string) (models.ModProject, bool) {
	return providers.LookupProjectByPlatformSlug(platform, slug, mcVersion, modLoader)
}

func (s *Service) LookupProjectByID(platform, projectID, mcVersion, modLoader string) (models.ModProject, bool) {
	return providers.LookupProjectByPlatformID(platform, projectID, mcVersion, modLoader)
}

func (s *Service) GetPinnedModVersion(platform string, modID string, minecraftVersion string, modLoader string) storage.PinnedMod {
	pin, _ := storage.GetPinnedMod(platform, modID, minecraftVersion, modLoader)
	return pin
}

func (s *Service) PinModVersion(req appstructs.ModVersionPinRequest) storage.PinnedMod {
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	modID := strings.ToLower(strings.TrimSpace(req.ModID))
	versionID := strings.TrimSpace(req.VersionID)
	mcVersion := strings.TrimSpace(req.MinecraftVersion)
	modLoader := strings.ToLower(strings.TrimSpace(req.ModLoader))

	if platform == "" || modID == "" || versionID == "" || mcVersion == "" || modLoader == "" {
		return storage.PinnedMod{}
	}

	existing, found := storage.GetPinnedMod(platform, modID, mcVersion, modLoader)
	if found && strings.EqualFold(existing.VersionID, versionID) {
		_ = storage.DeletePinnedMod(platform, modID, mcVersion, modLoader)
		return storage.PinnedMod{}
	}

	pin := storage.PinnedMod{
		Platform:         platform,
		ModID:            modID,
		VersionID:        versionID,
		MinecraftVersion: mcVersion,
		ModLoader:        modLoader,
	}
	if err := storage.UpsertPinnedMod(pin); err != nil {
		logging.Error("upsert pinned mod failed", "platform", platform, "modID", modID, "versionID", versionID, "minecraftVersion", mcVersion, "modLoader", modLoader, "error", err)
		return storage.PinnedMod{}
	}
	pin, _ = storage.GetPinnedMod(platform, modID, mcVersion, modLoader)
	return pin
}

func (s *Service) ListPinnedMods() []storage.PinnedMod {
	return storage.ListPinnedMods()
}

func (s *Service) UnpinMod(platform, modID, mcVersion, modLoader string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	modID = strings.ToLower(strings.TrimSpace(modID))
	mcVersion = strings.TrimSpace(mcVersion)
	modLoader = strings.ToLower(strings.TrimSpace(modLoader))
	if platform == "" || modID == "" || mcVersion == "" || modLoader == "" {
		return false
	}
	if _, found := storage.GetPinnedMod(platform, modID, mcVersion, modLoader); !found {
		return false
	}
	if err := storage.DeletePinnedMod(platform, modID, mcVersion, modLoader); err != nil {
		logging.Error("unpin mod failed", "platform", platform, "modID", modID, "minecraftVersion", mcVersion, "modLoader", modLoader, "error", err)
		return false
	}
	return true
}

func (s *Service) ListFavoriteLists() []storage.FavoriteList {
	return storage.ListFavoriteLists()
}

func (s *Service) CreateFavoriteList(name string) storage.FavoriteList {
	return s.CreateFavoriteListForScope(name, "", "")
}

func (s *Service) CreateFavoriteListForScope(name, minecraftVersion, modLoader string) storage.FavoriteList {
	list, err := storage.CreateFavoriteListForScope(name, minecraftVersion, modLoader)
	if err != nil {
		logging.Error("create favorite list failed", "name", name, "error", err)
		return storage.FavoriteList{}
	}
	return list
}

func (s *Service) RenameFavoriteList(id, name string) storage.FavoriteList {
	list, ok, err := storage.UpdateFavoriteListName(id, name)
	if err != nil {
		logging.Error("rename favorite list failed", "id", id, "name", name, "error", err)
		return storage.FavoriteList{}
	}
	if !ok {
		return storage.FavoriteList{}
	}
	return list
}

func (s *Service) DeleteFavoriteList(id string) bool {
	deleted, err := storage.DeleteFavoriteList(id)
	if err != nil {
		logging.Error("delete favorite list failed", "id", id, "error", err)
		return false
	}
	return deleted
}

func (s *Service) UpdateFavoriteListMetadata(list storage.FavoriteList) storage.FavoriteList {
	updated, ok, err := storage.UpdateFavoriteListMetadata(list)
	if err != nil {
		logging.Error("update favorite list metadata failed", "id", list.ID, "error", err)
		return storage.FavoriteList{}
	}
	if !ok {
		return storage.FavoriteList{}
	}
	return updated
}

func (s *Service) ReorderFavoriteLists(ids []string) bool {
	if err := storage.ReorderFavoriteLists(ids); err != nil {
		logging.Error("reorder favorite lists failed", "error", err)
		return false
	}
	return true
}

func (s *Service) ListFavoriteMods(listID string) []storage.FavoriteMod {
	return storage.ListFavoriteMods(listID)
}

func (s *Service) ListFavoriteContents(listID string) storage.FavoriteListContents {
	return storage.ListFavoriteContents(listID)
}

func (s *Service) AddFavoriteMod(mod storage.FavoriteMod) storage.FavoriteMod {
	_, existed := favoriteModKeySet(storage.ListFavoriteMods(mod.ListID))[favoriteModIdentityKey(mod)]
	saved, ok, err := storage.UpsertFavoriteMod(mod)
	if err != nil {
		logging.Error("add favorite mod failed", "listID", mod.ListID, "platform", mod.Platform, "modID", mod.ModID, "error", err)
		return storage.FavoriteMod{}
	}
	if !ok {
		return storage.FavoriteMod{}
	}
	if !existed {
		s.incrementUsageStat(storage.UsageStatFavoritesAdded, 1)
	}
	return saved
}

func (s *Service) AddFavoriteModsToLists(req FavoriteBulkAddRequest) FavoriteBulkOperationResult {
	targetListIDs := uniqueNonEmpty(req.TargetListIDs)
	if len(targetListIDs) == 0 || len(req.Mods) == 0 {
		return FavoriteBulkOperationResult{Skipped: len(req.Mods)}
	}

	lists := favoriteListIDSet()
	result := FavoriteBulkOperationResult{}
	for _, targetListID := range targetListIDs {
		if _, ok := lists[targetListID]; !ok {
			result.Skipped += len(req.Mods)
			result.Errors = append(result.Errors, "missing target list: "+targetListID)
			continue
		}

		existing := favoriteModKeySet(storage.ListFavoriteMods(targetListID))
		for _, mod := range req.Mods {
			copied := favoriteModForTargetList(mod, targetListID)
			if copied.Platform == "" || copied.ModID == "" {
				result.Skipped++
				continue
			}
			key := favoriteModIdentityKey(copied)
			_, wasExisting := existing[key]
			if _, ok, err := storage.UpsertFavoriteMod(copied); err != nil {
				logging.Error("bulk add favorite mod failed", "targetListID", targetListID, "platform", copied.Platform, "modID", copied.ModID, "error", err)
				result.Skipped++
				result.Errors = append(result.Errors, err.Error())
			} else if !ok {
				result.Skipped++
			} else if wasExisting {
				result.Updated++
			} else {
				result.Added++
				existing[key] = struct{}{}
			}
		}
	}
	if result.Added > 0 {
		s.incrementUsageStat(storage.UsageStatFavoritesAdded, int64(result.Added))
	}
	return result
}

func (s *Service) CopyFavoriteListToList(req FavoriteListCopyRequest) FavoriteBulkOperationResult {
	sourceListID := strings.TrimSpace(req.SourceListID)
	targetListID := strings.TrimSpace(req.TargetListID)
	if sourceListID == "" || targetListID == "" {
		return FavoriteBulkOperationResult{}
	}
	if sourceListID == targetListID {
		contents := storage.ListFavoriteContents(sourceListID)
		return FavoriteBulkOperationResult{
			Skipped: len(contents.Mods),
			Errors:  []string{"source and target lists are the same"},
		}
	}
	contents := storage.ListFavoriteContents(sourceListID)
	mods := make([]storage.FavoriteMod, 0, len(contents.Mods))
	for _, entry := range contents.Mods {
		mods = append(mods, entry.FavoriteMod)
	}
	return s.AddFavoriteModsToLists(FavoriteBulkAddRequest{
		TargetListIDs: []string{targetListID},
		Mods:          mods,
	})
}

func (s *Service) PreviewFavoriteListCrossVersionCopy(req FavoriteCrossVersionCopyRequest) FavoriteCrossVersionCopyPreview {
	sourceListID := strings.TrimSpace(req.SourceListID)
	minecraftVersion := strings.TrimSpace(req.MinecraftVersion)
	modLoader := strings.ToLower(strings.TrimSpace(req.ModLoader))
	preview := FavoriteCrossVersionCopyPreview{
		SourceListID:     sourceListID,
		MinecraftVersion: minecraftVersion,
		ModLoader:        modLoader,
	}
	if sourceListID == "" {
		preview.Errors = append(preview.Errors, "source list is required")
		return preview
	}
	if minecraftVersion == "" || modLoader == "" {
		preview.Errors = append(preview.Errors, "minecraft version and modloader are required")
		return preview
	}
	sourceList, ok := storage.GetFavoriteList(sourceListID)
	if !ok {
		preview.Errors = append(preview.Errors, "source list not found")
		return preview
	}
	preview.TargetListName = sourceList.Name
	if sourceList.MinecraftVersion == minecraftVersion && sourceList.ModLoader == modLoader {
		preview.Errors = append(preview.Errors, "target scope must differ from source scope")
		return preview
	}
	if _, found := storage.GetFavoriteListByNameAndScope(sourceList.Name, minecraftVersion, modLoader); found {
		preview.NameConflict = true
		preview.Errors = append(preview.Errors, "a favorite list with the same name already exists in the target scope")
		return preview
	}

	contents := storage.ListFavoriteContents(sourceListID)
	for _, entry := range contents.Mods {
		project, ok := s.favoriteCrossVersionCopyProject(entry.FavoriteMod, minecraftVersion, modLoader)
		if !ok {
			preview.Conflicts = append(preview.Conflicts, FavoriteCrossVersionCopyConflict{
				Source: entry,
				Reason: "project not found",
			})
			continue
		}
		versions := providers.ListMatchingProjectVersions(project, minecraftVersion, modLoader)
		if len(versions) == 0 {
			preview.Conflicts = append(preview.Conflicts, FavoriteCrossVersionCopyConflict{
				Source: entry,
				Reason: "matching version not found",
			})
			continue
		}
		target := favoriteModForTargetList(entry.FavoriteMod, "")
		target.MinecraftVersion = minecraftVersion
		target.ModLoader = modLoader
		target.VersionID = favoriteCrossVersionCopyVersionID(versions[0])
		if strings.TrimSpace(target.ModID) == "" {
			target.ModID = strings.TrimSpace(project.ProjectID)
		}
		if strings.TrimSpace(target.Platform) == "" {
			target.Platform = strings.TrimSpace(project.Platform)
		}
		if strings.TrimSpace(target.Slug) == "" {
			target.Slug = strings.TrimSpace(project.Slug)
		}
		preview.Matched = append(preview.Matched, FavoriteCrossVersionCopyMatch{
			Source:  entry,
			Target:  target,
			Project: project,
			Version: versions[0],
		})
	}
	return preview
}

func (s *Service) ApplyFavoriteListCrossVersionCopy(req FavoriteCrossVersionCopyRequest) FavoriteCrossVersionCopyApplyResult {
	preview := s.PreviewFavoriteListCrossVersionCopy(req)
	result := FavoriteCrossVersionCopyApplyResult{Preview: preview}
	if len(preview.Errors) > 0 {
		result.Result.Errors = append(result.Result.Errors, preview.Errors...)
		return result
	}
	if len(preview.Conflicts) > 0 && !req.IgnoreConflicts {
		result.Result.Skipped = len(preview.Matched) + len(preview.Conflicts)
		result.Result.Errors = append(result.Result.Errors, "copy conflicts must be ignored or resolved before applying")
		return result
	}
	if len(preview.Matched) == 0 {
		result.Result.Skipped = len(preview.Conflicts)
		result.Result.Errors = append(result.Result.Errors, "no matching mods to copy")
		return result
	}
	mods := make([]storage.FavoriteMod, 0, len(preview.Matched))
	for _, match := range preview.Matched {
		mods = append(mods, match.Target)
	}
	targetList, saved, err := storage.CreateFavoriteListWithModsForScope(
		preview.TargetListName,
		preview.MinecraftVersion,
		preview.ModLoader,
		mods,
	)
	if err != nil {
		if errors.Is(err, storage.ErrFavoriteListNameConflict) {
			result.Preview.NameConflict = true
			result.Result.Errors = append(result.Result.Errors, "a favorite list with the same name already exists in the target scope")
		} else {
			logging.Error("cross-version favorite copy failed", "sourceListID", preview.SourceListID, "minecraftVersion", preview.MinecraftVersion, "modLoader", preview.ModLoader, "error", err)
			result.Result.Errors = append(result.Result.Errors, err.Error())
		}
		result.Result.Skipped = len(preview.Matched) + len(preview.Conflicts)
		return result
	}
	result.TargetList = targetList
	result.Result.Added = len(saved)
	result.Result.Skipped = len(preview.Conflicts)
	result.Applied = result.Result.Added > 0
	if result.Applied {
		s.incrementUsageStat(storage.UsageStatFavoritesAdded, int64(result.Result.Added))
	}
	return result
}

func (s *Service) favoriteCrossVersionCopyProject(mod storage.FavoriteMod, minecraftVersion, modLoader string) (models.ModProject, bool) {
	platform := strings.TrimSpace(mod.Platform)
	modID := strings.TrimSpace(mod.ModID)
	slug := strings.TrimSpace(mod.Slug)
	if platform == "" {
		return models.ModProject{}, false
	}
	if modID != "" {
		if project, ok := storage.GetModPlatform(platform, modID); ok {
			return favoriteCrossVersionCopyProjectWithID(project), true
		}
		if project, ok := storage.GetModPlatformBySlug(platform, modID); ok {
			return favoriteCrossVersionCopyProjectWithID(project), true
		}
	}
	if slug != "" {
		if project, ok := storage.GetModPlatformBySlug(platform, slug); ok {
			return favoriteCrossVersionCopyProjectWithID(project), true
		}
		if project, ok := storage.GetModPlatform(platform, slug); ok {
			return favoriteCrossVersionCopyProjectWithID(project), true
		}
	}
	if modID != "" {
		if project, ok := favoriteCrossVersionCopyLookupProjectByID(platform, modID, minecraftVersion, modLoader); ok {
			return favoriteCrossVersionCopyProjectWithID(project), true
		}
	}
	if slug != "" {
		if project, ok := favoriteCrossVersionCopyLookupProjectBySlug(platform, slug, minecraftVersion, modLoader); ok {
			return favoriteCrossVersionCopyProjectWithID(project), true
		}
	}
	return models.ModProject{}, false
}

func favoriteCrossVersionCopyProjectWithID(project models.ModProject) models.ModProject {
	if strings.TrimSpace(project.ID) == "" {
		project.ID = models.ProjectKey(project.Platform, project.ProjectID)
	}
	return project
}

func favoriteCrossVersionCopyVersionID(version models.ModVersion) string {
	if id := strings.TrimSpace(version.VersionID); id != "" {
		return id
	}
	return strings.TrimSpace(version.ID)
}

func (s *Service) AddFavoriteListReference(parentListID, childListID string) storage.FavoriteListRef {
	ref, ok, err := storage.CreateFavoriteListRef(parentListID, childListID)
	if err != nil {
		logging.Error("add favorite list reference failed", "parentListID", parentListID, "childListID", childListID, "error", err)
		return storage.FavoriteListRef{}
	}
	if !ok {
		return storage.FavoriteListRef{}
	}
	return ref
}

func (s *Service) RemoveFavoriteListReference(parentListID, childListID string) bool {
	deleted, err := storage.DeleteFavoriteListRef(parentListID, childListID)
	if err != nil {
		logging.Error("remove favorite list reference failed", "parentListID", parentListID, "childListID", childListID, "error", err)
		return false
	}
	return deleted
}

func (s *Service) ListFavoriteListRefs(parentListID string) []storage.FavoriteListRef {
	return storage.ListFavoriteListRefs(parentListID)
}

func (s *Service) RemoveFavoriteMod(listID, platform, modID, mcVersion, modLoader string) bool {
	deleted, err := storage.DeleteFavoriteMod(listID, platform, modID, mcVersion, modLoader)
	if err != nil {
		logging.Error("remove favorite mod failed", "listID", listID, "platform", platform, "modID", modID, "error", err)
		return false
	}
	return deleted
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func favoriteListIDSet() map[string]struct{} {
	lists := storage.ListFavoriteLists()
	out := make(map[string]struct{}, len(lists))
	for _, list := range lists {
		if id := strings.TrimSpace(list.ID); id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func favoriteModKeySet(mods []storage.FavoriteMod) map[string]struct{} {
	out := make(map[string]struct{}, len(mods))
	for _, mod := range mods {
		if key := favoriteModIdentityKey(mod); key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}

func favoriteModForTargetList(mod storage.FavoriteMod, targetListID string) storage.FavoriteMod {
	mod.ID = ""
	mod.ListID = strings.TrimSpace(targetListID)
	mod.CreatedAt = 0
	mod.UpdatedAt = 0
	return mod
}

func favoriteModIdentityKey(mod storage.FavoriteMod) string {
	platform := strings.ToLower(strings.TrimSpace(mod.Platform))
	modID := strings.ToLower(strings.TrimSpace(mod.ModID))
	mcVersion := strings.TrimSpace(mod.MinecraftVersion)
	modLoader := strings.ToLower(strings.TrimSpace(mod.ModLoader))
	if platform == "" || modID == "" {
		return ""
	}
	return platform + "|" + modID + "|" + mcVersion + "|" + modLoader
}

func (s *Service) GetMinecraftReleaseVersions() []string {
	return minecraft.GetMinecraftReleaseVersions()
}

func (s *Service) GetPreferences() AppPreferences {
	if s.config == nil {
		return AppPreferences{
			Theme:                       configs.ThemeDark.String(),
			Language:                    configs.LanguageSystem.String(),
			AnimationMode:               configs.DefaultAnimationMode.String(),
			AnimationEnabled:            configs.DefaultAnimationsEnabled,
			AnimationDurationMultiplier: configs.DefaultAnimationDurationMultiplier,
		}
	}
	animationMode := s.config.Prefers.AnimationModeValue()
	return AppPreferences{
		Theme:                       s.config.Prefers.Theme.Normalized().String(),
		Language:                    s.config.Prefers.Language.Normalized().String(),
		AnimationMode:               animationMode.String(),
		AnimationEnabled:            animationMode != configs.AnimationModeOff,
		AnimationDurationMultiplier: s.config.Prefers.NormalizedAnimationDurationMultiplier(),
	}
}

func (s *Service) GetSettings() SettingsView {
	sv := SettingsView{
		Theme:                       configs.ThemeDark.String(),
		Language:                    configs.LanguageSystem.String(),
		SimpleMode:                  false,
		AnimationMode:               configs.DefaultAnimationMode.String(),
		AnimationEnabled:            configs.DefaultAnimationsEnabled,
		AnimationDurationMultiplier: configs.DefaultAnimationDurationMultiplier,
		AutoScanUnusedDependencies:  configs.DefaultAutoScanUnusedDependencies,
		FileConcurrency:             configs.DefaultFileConcurrency,
		ConcurrentDownloads:         configs.DefaultConcurrentDownloads,
		TargetDownloadRateMiB:       configs.DefaultTargetDownloadRateMiB,
	}
	if s.config != nil {
		animationMode := s.config.Prefers.AnimationModeValue()
		sv.Theme = s.config.Prefers.Theme.Normalized().String()
		sv.Language = s.config.Prefers.Language.Normalized().String()
		sv.SimpleMode = s.config.Prefers.SimpleMode
		sv.AnimationMode = animationMode.String()
		sv.AnimationEnabled = animationMode != configs.AnimationModeOff
		sv.AnimationDurationMultiplier = s.config.Prefers.NormalizedAnimationDurationMultiplier()
		sv.AutoScanUnusedDependencies = s.config.Prefers.AutoScanUnusedDependenciesValue()
		sv.MCIMEnabled = s.config.Prefers.MCIMEnabledValue()
		sv.FileConcurrency = s.config.Downloads.FileConcurrencyValue()
		sv.ConcurrentDownloads = s.config.Downloads.ConcurrentDownloadsValue()
		sv.AdaptiveFileConcurrency = s.config.Downloads.AdaptiveFileConcurrency
		sv.TargetDownloadRateMiB = s.config.Downloads.TargetDownloadRateMiBValue()
		sv.VerifySHA1 = s.config.Downloads.VerifySHA1
		sv.RequestsPerSecond = s.config.API.RequestsPerSecondValue()
		sv.MinecraftDir = minecraft.SimplifyPathWithEnv(minecraft.ExpandPathWithEnv(s.config.Prefers.MinecraftDir))
		sv.CacheDir = minecraft.SimplifyPathWithEnv(minecraft.ExpandPathWithEnv(s.config.Runtime.CacheDir))
		sv.HasModrinthKey = strings.TrimSpace(s.config.Keys.ModrinthApiKey) != ""
		sv.ModrinthKeyMask = MaskKey(s.config.Keys.ModrinthApiKey)
	}
	effectiveCFKey := s.effectiveCurseforgeAPIKey()
	sv.HasCurseforgeKey = effectiveCFKey != ""
	sv.CurseforgeKeyMask = MaskKey(effectiveCFKey)
	if sv.MinecraftDir == "" {
		sv.MinecraftDir = minecraft.SimplifyPathWithEnv(global.GetMinecraftDir())
	}
	if path, err := s.cachePath(); err == nil {
		sv.CachePath = minecraft.SimplifyPathWithEnv(path)
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

func (s *Service) SaveLanguage(language string) string {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	parsed := configs.ParseLanguage(language)
	if parsed == "" {
		parsed = configs.LanguageSystem
	}
	s.config.Prefers.Language = parsed
	if err := configs.Save(s.config); err != nil {
		logging.Error("save language failed", "language", parsed, "error", err)
	}
	return parsed.String()
}

func (s *Service) SaveAnimationSettings(req SaveAnimationSettingsRequest) SettingsView {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	var animationMode configs.AnimationMode
	if strings.TrimSpace(req.AnimationMode) != "" {
		animationMode = configs.ParseAnimationMode(req.AnimationMode)
	}
	if animationMode == "" {
		if req.AnimationEnabled {
			animationMode = configs.AnimationModeVuetify
		} else {
			animationMode = configs.AnimationModeOff
		}
	}
	s.config.Prefers.AnimationMode = animationMode
	s.config.Prefers.AnimationsEnabled = configs.BoolPtr(animationMode != configs.AnimationModeOff)
	s.config.Prefers.AnimationDurationMultiplier = configs.NormalizeAnimationDurationMultiplier(req.AnimationDurationMultiplier)
	if err := configs.Save(s.config); err != nil {
		logging.Error("save animation settings failed", "animationMode", animationMode, "animationDurationMultiplier", req.AnimationDurationMultiplier, "error", err)
	}
	return s.GetSettings()
}

func (s *Service) SaveSimpleModeSettings(req SaveSimpleModeSettingsRequest) SettingsView {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	s.config.Prefers.SimpleMode = req.SimpleMode
	if err := configs.Save(s.config); err != nil {
		logging.Error("save simple mode setting failed", "simpleMode", req.SimpleMode, "error", err)
	}
	configureSimpleMode(req.SimpleMode, s.downloadEvents())
	return s.GetSettings()
}

func (s *Service) SaveUnusedDependencyCleanupSettings(req SaveUnusedDependencyCleanupSettingsRequest) SettingsView {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	s.config.Prefers.AutoScanUnusedDependencies = configs.BoolPtr(req.AutoScanUnusedDependencies)
	if err := configs.Save(s.config); err != nil {
		logging.Error("save unused dependency cleanup settings failed", "autoScanUnusedDependencies", req.AutoScanUnusedDependencies, "error", err)
	}
	return s.GetSettings()
}

func (s *Service) SaveMCIMSettings(req SaveMCIMSettingsRequest) SettingsView {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	s.config.Prefers.MCIMEnabled = req.MCIMEnabled
	if err := configs.Save(s.config); err != nil {
		logging.Error("save MCIM settings failed", "mcimEnabled", req.MCIMEnabled, "error", err)
	}
	s.configureProviderClients()
	downloader.Configure(downloader.Config{
		FileConcurrency:         s.config.Downloads.FileConcurrencyValue(),
		ConcurrentDownloads:     s.config.Downloads.ConcurrentDownloadsValue(),
		AdaptiveFileConcurrency: s.config.Downloads.AdaptiveFileConcurrency,
		TargetDownloadRateMiB:   s.config.Downloads.TargetDownloadRateMiBValue(),
		VerifySHA1:              s.config.Downloads.VerifySHA1,
	})
	return s.GetSettings()
}

func (s *Service) SaveNetworkSettings(req SaveNetworkSettingsRequest) SettingsView {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	s.config.Downloads.FileConcurrency = configs.DownloadConfig{FileConcurrency: req.FileConcurrency}.FileConcurrencyValue()
	s.config.Downloads.ConcurrentDownloads = configs.DownloadConfig{ConcurrentDownloads: req.ConcurrentDownloads}.ConcurrentDownloadsValue()
	s.config.Downloads.AdaptiveFileConcurrency = req.AdaptiveFileConcurrency
	s.config.Downloads.TargetDownloadRateMiB = configs.DownloadConfig{TargetDownloadRateMiB: req.TargetDownloadRateMiB}.TargetDownloadRateMiBValue()
	s.config.Downloads.VerifySHA1 = req.VerifySHA1
	s.config.API.RequestsPerSecond = configs.APIConfig{RequestsPerSecond: req.RequestsPerSecond}.RequestsPerSecondValue()
	if err := configs.Save(s.config); err != nil {
		logging.Error("save network settings failed", "error", err)
	}
	downloader.Configure(downloader.Config{
		FileConcurrency:         s.config.Downloads.FileConcurrency,
		ConcurrentDownloads:     s.config.Downloads.ConcurrentDownloads,
		AdaptiveFileConcurrency: s.config.Downloads.AdaptiveFileConcurrency,
		TargetDownloadRateMiB:   s.config.Downloads.TargetDownloadRateMiB,
		VerifySHA1:              s.config.Downloads.VerifySHA1,
	})
	configureRemoteModIDConcurrency(s.config.Downloads.ConcurrentDownloads)
	s.configureProviderClients()
	return s.GetSettings()
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

func (s *Service) SaveCacheDirPreference(dir string) SettingsView {
	if s.config == nil {
		s.config = &configs.Config{}
	}
	dir = strings.TrimSpace(dir)
	if dir != "" {
		dir = minecraft.SimplifyPathWithEnv(minecraft.ExpandPathWithEnv(dir))
	}
	s.config.Runtime.CacheDir = dir
	if err := configs.Save(s.config); err != nil {
		logging.Error("save cache dir preference failed", "cacheDir", dir, "error", err)
	}
	if cachePath, err := s.cachePath(); err != nil {
		logging.Error("resolve database cache path after cache dir save failed", "cacheDir", dir, "error", err)
	} else if err := storage.OpenAt(cachePath); err != nil {
		logging.Error("reopen database after cache dir save failed", "cachePath", cachePath, "error", err)
	}
	return s.GetSettings()
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
	return downloader.QueueModDownloadWithUserAgent(s.ctx, req, s.effectiveCurseforgeAPIKey(), s.UserAgent(), s.downloadEvents())
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

	result := downloader.QueueModDownloadWithUserAgent(ctx, req, s.effectiveCurseforgeAPIKey(), s.UserAgent(), events)
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

func (s *Service) GetUsageStats() storage.UsageStats {
	return storage.GetUsageStats()
}

func (s *Service) CancelDownload(id string) bool {
	return downloader.CancelDownload(s.ctx, id, s.downloadEvents())
}

func (s *Service) RetryDownload(id string) bool {
	return downloader.RetryDownload(s.ctx, id, s.downloadEvents())
}

func (s *Service) RemoveDownload(id string) bool {
	return downloader.RemoveDownload(id, s.downloadEvents())
}

func (s *Service) DismissOptionalDependencyReminder(id string) bool {
	return downloader.DismissOptionalDependencyReminder(id, s.downloadEvents())
}

func (s *Service) ClearOptionalDependencyReminders() bool {
	return downloader.ClearOptionalDependencyReminders(s.downloadEvents())
}

func (s *Service) InstallOptionalDependencies(id string) []appstructs.ModDownloadResult {
	return downloader.InstallOptionalDependenciesWithUserAgent(s.ctx, id, s.effectiveCurseforgeAPIKey(), s.UserAgent(), s.downloadEvents())
}

func (s *Service) AnalyzeBatchIncompatibleConflicts(req appstructs.BatchDownloadRequest) appstructs.BatchIncompatibleAnalysis {
	req = s.applyRuntimeBatchDownloadRequest(req)
	return downloader.AnalyzeBatchIncompatibleConflicts(req)
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
	s.stopModsWatcher()
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
			s.ensureModsWatcher(global.GetSelectedVersion())
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
	refreshed := s.refreshAndSelectVersionMods(selected, global.GetMinecraftDir())
	s.ensureModsWatcher(refreshed)
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
		version = resetConnectorVersion(version)
		if !ValidMinecraftInstance(version) {
			return structs.VersionInfo{}, errors.New("invalid minecraft version or mod loader")
		}
		selected := s.refreshAndSelectVersionMods(version, mcDir)
		s.ensureModsWatcher(selected)
		return selected, nil
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
	enriched, _ := enrichModMetadata(mods)
	return enriched, nil
}

func (s *Service) ApplyLocalModBatchOperation(req appstructs.LocalModBatchOperationRequest) (structs.VersionInfo, error) {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	selected := global.GetSelectedVersion()
	if selected.ID == "" && selected.Name == "" {
		return structs.VersionInfo{}, errors.New("no selected version")
	}

	versionDir := minecraft.VersionDirPath(global.GetMinecraftDir(), selected)
	if strings.TrimSpace(versionDir) == "" {
		return s.RefreshSelectedVersionMods(), errors.New("selected version directory not found")
	}
	modsDir := filepath.Join(versionDir, "mods")

	ops, err := planLocalModBatchOperations(req.Paths, action, modsDir, global.GetMinecraftDir())
	if err != nil {
		return s.RefreshSelectedVersionMods(), err
	}
	if err := applyLocalModFileOperations(ops); err != nil {
		return s.RefreshSelectedVersionMods(), err
	}
	s.recordLocalModUsage(ops)
	return s.refreshChangedLocalMods(ops), nil
}

func (s *Service) refreshChangedLocalMods(ops []localModFileOperation) structs.VersionInfo {
	selected := global.GetSelectedVersion()
	if selected.ID == "" && selected.Name == "" {
		return selected
	}
	instanceID := versionInstanceDir(selected)
	mcDir := global.GetMinecraftDir()
	changed := make(map[string]bool)
	for _, op := range ops {
		removeLocalModPathVariants(op.source, mcDir)
		removeLocalModPathVariants(op.target, mcDir)
		changed[op.source], changed[op.target] = true, true
		if op.action == LocalModBatchActionDelete {
			continue
		}
		if strings.HasSuffix(op.target, ".jar") {
			minecraft.ScanModFile(op.target, instanceID, selected.MinecraftVersion, selected.ActualModLoader, mcDir)
		}
	}
	mods := make([]structs.ModInfo, 0, len(selected.Mods))
	for _, mod := range selected.Mods {
		absPath := mod.Path
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(mcDir, absPath)
		}
		if !changed[absPath] && !changed[mod.Path] {
			mods = append(mods, mod)
		}
	}
	for _, op := range ops {
		if op.action == LocalModBatchActionDelete || !strings.HasSuffix(op.target, ".jar") {
			continue
		}
		fresh := minecraft.ScanModFile(op.target, instanceID, selected.MinecraftVersion, selected.ActualModLoader, mcDir)
		mods = append(mods, fresh...)
	}
	enriched, missing := enrichModMetadata(mods)
	setOnlineMetadataLoading(enriched, missing, true)
	selected.Mods = enriched
	selected = reconcileConnectorVersion(selected)
	global.SetSelectedVersion(selected)
	s.emit(EventSelectedVersionChanged, selected)
	s.resolveMissingModMetadata(selected, missing)
	return selected
}

func (s *Service) refreshChangedPaths(paths []string) {
	selected := global.GetSelectedVersion()
	if selected.ID == "" && selected.Name == "" {
		return
	}
	instanceID := versionInstanceDir(selected)
	mcDir := global.GetMinecraftDir()
	changed := make(map[string]bool)
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		removeLocalModPathVariants(abs, mcDir)
		changed[abs] = true
	}
	mods := make([]structs.ModInfo, 0, len(selected.Mods))
	for _, mod := range selected.Mods {
		abs := mod.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(mcDir, abs)
		}
		if !changed[abs] {
			mods = append(mods, mod)
		}
	}
	for _, path := range paths {
		if strings.HasSuffix(strings.ToLower(path), ".jar") {
			mods = append(mods, minecraft.ScanModFile(path, instanceID, selected.MinecraftVersion, selected.ActualModLoader, mcDir)...)
		}
	}
	enriched, missing := enrichModMetadata(mods)
	setOnlineMetadataLoading(enriched, missing, true)
	selected.Mods = enriched
	selected = reconcileConnectorVersion(selected)
	global.SetSelectedVersion(selected)
	s.emit(EventSelectedVersionChanged, selected)
	s.resolveMissingModMetadata(selected, missing)
}

func removeLocalModPathVariants(path, mcDir string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	global.RemoveLocalModByPath(path)
	if abs, err := filepath.Abs(path); err == nil {
		global.RemoveLocalModByPath(abs)
		if strings.TrimSpace(mcDir) != "" {
			if rel, err := filepath.Rel(mcDir, abs); err == nil {
				global.RemoveLocalModByPath(rel)
			}
		}
	}
}

func (s *Service) recordLocalModUsage(ops []localModFileOperation) {
	counts := make(map[storage.UsageStatKey]int64)
	for _, op := range ops {
		switch op.action {
		case LocalModBatchActionEnable:
			counts[storage.UsageStatModsEnabled]++
		case LocalModBatchActionDisable:
			counts[storage.UsageStatModsDisabled]++
		case LocalModBatchActionDelete:
			counts[storage.UsageStatModsDeleted]++
		}
	}
	for key, count := range counts {
		s.incrementUsageStat(key, count)
	}
}

type localModFileOperation struct {
	action string
	source string
	target string
}

func planLocalModBatchOperations(paths []string, action string, modsDir string, minecraftDir string) ([]localModFileOperation, error) {
	switch action {
	case LocalModBatchActionEnable, LocalModBatchActionDisable, LocalModBatchActionInvert, LocalModBatchActionDelete:
	default:
		return nil, fmt.Errorf("unsupported local mod action: %s", action)
	}
	if len(paths) == 0 {
		return nil, errors.New("no local mod paths selected")
	}

	modsDir, err := filepath.Abs(filepath.Clean(modsDir))
	if err != nil {
		return nil, err
	}
	minecraftDir = strings.TrimSpace(minecraftDir)
	if minecraftDir != "" {
		if abs, err := filepath.Abs(filepath.Clean(minecraftDir)); err == nil {
			minecraftDir = abs
		}
	}

	seen := make(map[string]bool, len(paths))
	targets := make(map[string]string, len(paths))
	ops := make([]localModFileOperation, 0, len(paths))
	for _, rawPath := range paths {
		source, err := resolveSelectedLocalModPath(rawPath, modsDir, minecraftDir)
		if err != nil {
			return nil, err
		}
		if seen[source] {
			continue
		}
		seen[source] = true

		info, err := os.Stat(source)
		if err != nil {
			return nil, fmt.Errorf("local mod file not found: %s", rawPath)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("local mod path is a directory: %s", rawPath)
		}

		fileAction := action
		if action == LocalModBatchActionInvert {
			if localModPathDisabled(source) {
				fileAction = LocalModBatchActionEnable
			} else {
				fileAction = LocalModBatchActionDisable
			}
		}

		op := localModFileOperation{action: fileAction, source: source}
		switch fileAction {
		case LocalModBatchActionEnable:
			if !localModPathDisabled(source) {
				continue
			}
			op.target = strings.TrimSuffix(source, ".disabled")
			if err := validateLocalModRenameTarget(source, op.target, targets); err != nil {
				return nil, err
			}
		case LocalModBatchActionDisable:
			if localModPathDisabled(source) {
				continue
			}
			op.target = source + ".disabled"
			if err := validateLocalModRenameTarget(source, op.target, targets); err != nil {
				return nil, err
			}
		case LocalModBatchActionDelete:
		default:
			return nil, fmt.Errorf("unsupported local mod action: %s", fileAction)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func applyLocalModFileOperations(ops []localModFileOperation) error {
	for _, op := range ops {
		switch op.action {
		case LocalModBatchActionEnable, LocalModBatchActionDisable:
			if err := os.Rename(op.source, op.target); err != nil {
				return err
			}
			global.RemoveLocalModByPath(op.source)
		case LocalModBatchActionDelete:
			if err := os.Remove(op.source); err != nil {
				return err
			}
			global.RemoveLocalModByPath(op.source)
		default:
			return fmt.Errorf("unsupported local mod action: %s", op.action)
		}
	}
	return nil
}

func resolveSelectedLocalModPath(rawPath string, modsDir string, minecraftDir string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", errors.New("empty local mod path")
	}

	candidates := []string{rawPath}
	if !filepath.IsAbs(rawPath) {
		if minecraftDir != "" {
			candidates = append(candidates, filepath.Join(minecraftDir, rawPath))
		}
		candidates = append(candidates, filepath.Join(modsDir, rawPath))
	}

	missingInside := false
	for _, candidate := range candidates {
		abs, err := filepath.Abs(filepath.Clean(candidate))
		if err != nil {
			continue
		}
		if !pathInsideDir(abs, modsDir) {
			continue
		}
		if !isLocalModJarPath(abs) {
			return "", fmt.Errorf("local mod path is not a jar: %s", rawPath)
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		} else if os.IsNotExist(err) {
			missingInside = true
		}
	}

	if missingInside {
		return "", fmt.Errorf("local mod file not found: %s", rawPath)
	}
	return "", fmt.Errorf("local mod path is outside selected instance: %s", rawPath)
}

func validateLocalModRenameTarget(source string, target string, plannedTargets map[string]string) error {
	if existingSource, ok := plannedTargets[target]; ok && existingSource != source {
		return fmt.Errorf("multiple local mod files target the same path: %s", target)
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("local mod target already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	plannedTargets[target] = source
	return nil
}

func isLocalModJarPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".jar") || strings.HasSuffix(lower, ".jar.disabled")
}

func localModPathDisabled(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".jar.disabled")
}

func pathInsideDir(path string, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Service) LoadVersionsFromDisk(mcDir string) []structs.VersionInfo {
	if strings.TrimSpace(mcDir) == "" {
		global.SetVersionsForDir(mcDir, nil)
		return nil
	}

	infos := minecraft.LoadLauncherVersions(mcDir, loadMinecraftDirVersions)
	for i := range infos {
		infos[i] = resetConnectorVersion(infos[i])
	}
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
		return filepath.Join(s.resolveRuntimePath(cacheDir), storage.CacheFileName), nil
	}
	if cacheDir := s.resolveRuntimePath(s.options.Runtime.DefaultCacheDir); cacheDir != "" {
		return filepath.Join(cacheDir, storage.CacheFileName), nil
	}
	return storage.DefaultCachePath()
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

func (s *Service) applyRuntimeBatchDownloadRequest(req appstructs.BatchDownloadRequest) appstructs.BatchDownloadRequest {
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

func (s *Service) effectiveCurseforgeAPIKey() string {
	if s == nil || s.config == nil {
		return configs.EffectiveCurseforgeAPIKey("")
	}
	return configs.EffectiveCurseforgeAPIKey(s.config.Keys.CurseforgeApiKey)
}

func (s *Service) configureProviderClients() {
	limiter := newRequestRateLimiter(s.Config().API.RequestsPerSecondValue())
	limitedTransport := rateLimitedTransport{limiter: limiter, base: networking.NewTransport()}
	useMCIM := s.Config().Prefers.MCIMEnabledValue()
	curseForgeAPIKey := s.effectiveCurseforgeAPIKey()
	if curseForgeAPIKey != "" || useMCIM {
		apiBaseURL := ""
		if useMCIM {
			apiBaseURL = mcimCurseForgeBaseURL
		}
		global.SetCurseForgeClient(&curseforge.Client{API: curseforgeapi.New(curseForgeTransport{
			apiKey:     curseForgeAPIKey,
			userAgent:  s.UserAgent(),
			apiBaseURL: apiBaseURL,
			base:       limitedTransport,
		})})
	} else {
		global.SetCurseForgeClient(nil)
	}
	modrinthClient := modrinth.NewClient(&http.Client{Timeout: 10 * time.Second, Transport: limitedTransport})
	modrinthClient.UserAgent = s.UserAgent()
	if useMCIM {
		modrinthClient.BaseURL, _ = url.Parse(mcimModrinthBaseURL)
	}
	global.SetModrinthClient(modrinthClient)
}

func (s *Service) configureLogging() {
	s.configureLoggingConfig(s.Config().Logging)
}

func (s *Service) configureLoggingConfig(config configs.LoggingConfig) {
	path := logging.DefaultFileName
	if workDir := strings.TrimSpace(s.options.Runtime.WorkDir); workDir != "" {
		path = filepath.Join(workDir, logging.DefaultFileName)
	}
	if err := logging.Configure(logging.Options{
		Disabled:  !config.EnabledValue(),
		ForceFile: config.ForceFile,
		FilePath:  path,
	}); err != nil {
		logging.Error("configure logging failed", "path", path, "error", err)
	}
}

func (s *Service) downloadEvents() downloader.Events {
	return downloader.Events{
		OnQueueState: func(state appstructs.DownloadQueueState) {
			s.emit(EventDownloadQueueUpdated, state)
		},
		OnDownloadFailed: func(event appstructs.DownloadFailedEvent) {
			s.emit(EventDownloadFailed, event)
		},
		OnDownloadCompleted: func() {
			s.incrementUsageStat(storage.UsageStatDownloadsCompleted, 1)
			s.emit(EventDownloadCompleted, nil)
		},
	}
}

func (s *Service) incrementUsageStat(key storage.UsageStatKey, delta int64) {
	if err := storage.IncrementUsageStat(key, delta); err != nil {
		logging.Warn("increment usage stat failed", "key", key, "delta", delta, "error", err)
		return
	}
	s.emit(EventUsageStatsUpdated, storage.GetUsageStats())
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
	version = initializeActualModLoader(version)
	versionDirName := versionInstanceDir(version)
	if strings.TrimSpace(mcDir) == "" || strings.TrimSpace(versionDirName) == "" {
		version.Mods = nil
		return reconcileConnectorVersion(version)
	}
	versionDir := minecraft.VersionDirPath(mcDir, version)
	version.Mods = minecraft.ScanVersionMods(versionDir, versionDirName, version.MinecraftVersion, version.ActualModLoader, mcDir)
	return reconcileConnectorVersion(version)
}

func (s *Service) refreshAndSelectVersionMods(version structs.VersionInfo, mcDir string) structs.VersionInfo {
	refreshed := s.refreshVersionMods(version, mcDir)
	enriched, missingFiles := enrichModMetadata(refreshed.Mods)
	setOnlineMetadataLoading(enriched, missingFiles, true)
	refreshed.Mods = enriched
	global.SetSelectedVersion(refreshed)
	s.emit(EventSelectedVersionChanged, refreshed)
	s.resolveMissingModMetadata(refreshed, missingFiles)
	return refreshed
}

func (s *Service) stopModsWatcher() {
	s.modsWatchMu.Lock()
	defer s.modsWatchMu.Unlock()
	if s.modsWatchCancel != nil {
		s.modsWatchCancel()
		s.modsWatchCancel = nil
	}
	s.modsWatchDir = ""
}

func (s *Service) ensureModsWatcher(version structs.VersionInfo) {
	dir := filepath.Join(minecraft.VersionDirPath(global.GetMinecraftDir(), version), "mods")
	dir, err := filepath.Abs(dir)
	if err != nil || strings.TrimSpace(dir) == "" {
		return
	}
	s.modsWatchMu.Lock()
	if s.modsWatchDir == dir && s.modsWatchCancel != nil {
		s.modsWatchMu.Unlock()
		return
	}
	if s.modsWatchCancel != nil {
		s.modsWatchCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.modsWatchCancel = cancel
	s.modsWatchDir = dir
	s.modsWatchMu.Unlock()
	go s.watchModsDir(ctx, dir, version.ID, version.Name)
}

func (s *Service) watchModsDir(ctx context.Context, dir, versionID, versionName string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logging.Warn("create mods watcher failed", "dir", dir, "error", err)
		return
	}
	defer watcher.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logging.Warn("create mods directory for watcher failed", "dir", dir, "error", err)
		return
	}
	if err := watcher.Add(dir); err != nil {
		logging.Warn("watch mods directory failed", "dir", dir, "error", err)
		return
	}
	var timer *time.Timer
	pending := make(map[string]bool)
	var pendingMu sync.Mutex
	trigger := func(path string) {
		pendingMu.Lock()
		pending[path] = true
		pendingMu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(250*time.Millisecond, func() {
			selected := global.GetSelectedVersion()
			if selected.ID != versionID && selected.Name != versionName {
				return
			}
			s.modsWatchMu.Lock()
			activeDir := s.modsWatchDir
			s.modsWatchMu.Unlock()
			if ctx.Err() != nil || activeDir != dir {
				return
			}
			pendingMu.Lock()
			paths := make([]string, 0, len(pending))
			for path := range pending {
				paths = append(paths, path)
			}
			pending = make(map[string]bool)
			pendingMu.Unlock()
			s.refreshChangedPaths(paths)
		})
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logging.Warn("mods watcher error", "dir", dir, "error", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				trigger(event.Name)
			}
		}
	}
}

func (s *Service) resolveMissingModMetadata(refreshed structs.VersionInfo, missingFiles map[string]uint32) {
	if len(missingFiles) == 0 {
		return
	}
	go func() {
		projects, versions := providers.ResolveMetadataByLocalFiles(missingFiles)
		updated := global.GetSelectedVersion()
		if updated.ID != refreshed.ID || updated.Name != refreshed.Name {
			return
		}
		mods, changed := applyResolvedModMetadata(updated.Mods, missingFiles, projects, versions)
		if !changed {
			return
		}
		updated.Mods = mods
		global.SetSelectedVersion(updated)
		s.emit(EventSelectedVersionChanged, updated)
	}()
}

func applyResolvedModMetadata(mods []structs.ModInfo, missingFiles map[string]uint32, projects map[string]models.ModProject, versions map[string]models.ModVersion) ([]structs.ModInfo, bool) {
	updated := make([]structs.ModInfo, len(mods))
	copy(updated, mods)
	changed := false
	for i := range updated {
		sha1 := strings.ToLower(strings.TrimSpace(updated[i].SHA1))
		if _, pending := missingFiles[sha1]; !pending {
			continue
		}
		if project, ok := projects[sha1]; ok {
			enrichedMod := modbridge.ApplyPlatformMetadataToModInfo(updated[i], project, versions[sha1])
			if modInfoDisplayMetadataChanged(updated[i], enrichedMod) {
				updated[i] = enrichedMod
				changed = true
			}
		}
		if updated[i].OnlineMetadataLoading {
			updated[i].OnlineMetadataLoading = false
			changed = true
		}
	}
	return updated, changed
}

func setOnlineMetadataLoading(mods []structs.ModInfo, files map[string]uint32, loading bool) {
	for i := range mods {
		sha1 := strings.ToLower(strings.TrimSpace(mods[i].SHA1))
		if _, ok := files[sha1]; ok {
			mods[i].OnlineMetadataLoading = loading
		}
	}
}

func enrichModMetadata(mods []structs.ModInfo) ([]structs.ModInfo, map[string]uint32) {
	enriched := make([]structs.ModInfo, len(mods))
	copy(enriched, mods)
	missingFiles := make(map[string]uint32)
	for i := range enriched {
		sha1 := strings.ToLower(strings.TrimSpace(enriched[i].SHA1))
		if sha1 == "" {
			continue
		}
		project, version, ok := modbridge.PlatformMetadataForSHA1(sha1)
		if ok && hasProjectDisplayMetadata(project) {
			enriched[i] = modbridge.ApplyPlatformMetadataToModInfo(enriched[i], project, version)
		} else {
			missingFiles[sha1] = enriched[i].CfFingerprint
		}
	}
	return enriched, missingFiles
}

func hasProjectDisplayMetadata(project models.ModProject) bool {
	return strings.TrimSpace(project.Title) != "" ||
		strings.TrimSpace(project.IconURL) != "" ||
		len(project.Categories) > 0
}

func modInfoDisplayMetadataChanged(left, right structs.ModInfo) bool {
	if left.OnlineName != right.OnlineName ||
		left.OnlinePlatform != right.OnlinePlatform ||
		left.OnlineProjectID != right.OnlineProjectID ||
		left.OnlineSlug != right.OnlineSlug ||
		left.OnlineVersion != right.OnlineVersion ||
		left.OnlineVersionID != right.OnlineVersionID ||
		left.OnlineFileName != right.OnlineFileName ||
		left.OnlineMetadataLoading != right.OnlineMetadataLoading ||
		left.IconURL != right.IconURL ||
		len(left.Categories) != len(right.Categories) {
		return true
	}
	for i := range left.Categories {
		if left.Categories[i] != right.Categories[i] {
			return true
		}
	}
	return false
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
