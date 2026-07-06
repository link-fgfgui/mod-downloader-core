package database

import (
	"sort"
	"strings"
	"time"

	"github.com/link-fgfgui/mod-downloader-core/logging"
	"github.com/link-fgfgui/mod-downloader-core/models"
)

// --- record types ---

type PlatformAssociation struct {
	ID                  string `json:"id"`
	CurseForgeProjectID string `json:"curseforgeProjectId,omitempty"`
	ModrinthProjectID   string `json:"modrinthProjectId,omitempty"`
}

type PinnedMod struct {
	ID               string `json:"id"`
	Platform         string `json:"platform"`
	ModID            string `json:"modId"`
	VersionID        string `json:"versionId"`
	MinecraftVersion string `json:"minecraftVersion"`
	ModLoader        string `json:"modLoader"`
}

type FavoriteList struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	SortOrder int    `json:"sortOrder"`
	System    bool   `json:"system,omitempty"`
}

type FavoriteMod struct {
	ID               string   `json:"id"`
	ListID           string   `json:"listId"`
	Platform         string   `json:"platform"`
	ModID            string   `json:"modId"`
	VersionID        string   `json:"versionId,omitempty"`
	MinecraftVersion string   `json:"minecraftVersion,omitempty"`
	ModLoader        string   `json:"modLoader,omitempty"`
	Title            string   `json:"title,omitempty"`
	Slug             string   `json:"slug,omitempty"`
	IconURL          string   `json:"iconUrl,omitempty"`
	Description      string   `json:"description,omitempty"`
	Categories       []string `json:"categories,omitempty"`
	CreatedAt        int64    `json:"createdAt"`
	UpdatedAt        int64    `json:"updatedAt"`
}

type ModPlatformVersionScope struct {
	MinecraftVersion string
	ModLoader        string
}

type storedVersionScope struct {
	Platform         string `json:"platform"`
	ProjectID        string `json:"projectId"`
	MinecraftVersion string `json:"minecraftVersion"`
	ModLoader        string `json:"modLoader"`
	UpdatedAt        int64  `json:"updatedAt"`
}

func normalizePinnedModKey(platform, modID, mcVersion, modLoader string) (string, string, string, string) {
	return strings.ToLower(strings.TrimSpace(platform)),
		strings.ToLower(strings.TrimSpace(modID)),
		strings.TrimSpace(mcVersion),
		strings.ToLower(strings.TrimSpace(modLoader))
}

func normalizePinnedMod(p PinnedMod) PinnedMod {
	p.Platform, p.ModID, p.MinecraftVersion, p.ModLoader = normalizePinnedModKey(p.Platform, p.ModID, p.MinecraftVersion, p.ModLoader)
	p.ID = strings.TrimSpace(p.ID)
	p.VersionID = strings.TrimSpace(p.VersionID)
	return p
}

func normalizeFavoriteList(list FavoriteList) FavoriteList {
	list.ID = strings.TrimSpace(list.ID)
	list.Name = strings.TrimSpace(list.Name)
	return list
}

func normalizeFavoriteMod(mod FavoriteMod) FavoriteMod {
	mod.ID = strings.TrimSpace(mod.ID)
	mod.ListID = strings.TrimSpace(mod.ListID)
	mod.Platform, mod.ModID, mod.MinecraftVersion, mod.ModLoader = normalizePinnedModKey(mod.Platform, mod.ModID, mod.MinecraftVersion, mod.ModLoader)
	mod.VersionID = strings.TrimSpace(mod.VersionID)
	mod.Title = strings.TrimSpace(mod.Title)
	mod.Slug = strings.TrimSpace(mod.Slug)
	mod.IconURL = strings.TrimSpace(mod.IconURL)
	mod.Description = strings.TrimSpace(mod.Description)
	mod.Categories = models.NormalizeCategories(mod.Categories)
	return mod
}

func makePlatformKey(platform, projectID string) platformKey {
	return platformKey{Platform: normalizePlatform(platform), ProjectID: strings.TrimSpace(projectID)}
}

func makeVersionKey(platform, projectID, versionID string) versionKey {
	return versionKey{Platform: normalizePlatform(platform), ProjectID: strings.TrimSpace(projectID), VersionID: strings.TrimSpace(versionID)}
}

func makePinnedModKey(platform, modID, mcVersion, modLoader string) pinnedModKey {
	platform, modID, mcVersion, modLoader = normalizePinnedModKey(platform, modID, mcVersion, modLoader)
	return pinnedModKey{Platform: platform, ModID: modID, MinecraftVersion: mcVersion, ModLoader: modLoader}
}

func makeFavoriteModKey(listID, platform, modID, mcVersion, modLoader string) favoriteModKey {
	platform, modID, mcVersion, modLoader = normalizePinnedModKey(platform, modID, mcVersion, modLoader)
	return favoriteModKey{
		ListID:           strings.TrimSpace(listID),
		Platform:         platform,
		ModID:            modID,
		MinecraftVersion: mcVersion,
		ModLoader:        modLoader,
	}
}

func makeVersionScopeKey(platform, projectID string, scope ModPlatformVersionScope) versionScopeKey {
	return versionScopeKey{
		Platform:         normalizePlatform(platform),
		ProjectID:        strings.TrimSpace(projectID),
		MinecraftVersion: scope.MinecraftVersion,
		ModLoader:        scope.ModLoader,
	}
}

func normalizePlatform(platform string) string {
	return strings.ToLower(strings.TrimSpace(platform))
}

// --- mod platforms ---

func UpsertModPlatform(p models.ModProject) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	p = copyProject(p)
	p.Platform = strings.TrimSpace(p.Platform)
	p.ProjectID = strings.TrimSpace(p.ProjectID)
	if p.Platform == "" || p.ProjectID == "" {
		return nil
	}

	isFullMetadata := strings.TrimSpace(p.Title) != ""

	err = d.update(func(state *cacheState, pool *stringPool) error {
		key := internPlatformKey(pool, makePlatformKey(p.Platform, p.ProjectID))
		if existing, ok := state.ModPlatforms[key]; ok {
			if strings.TrimSpace(p.Slug) == "" {
				p.Slug = existing.Slug
			}
			p.UpdatedAt = existing.UpdatedAt
			if isFullMetadata {
				p.CachedAt = time.Now().Unix()
			} else {
				p.Title = existing.Title
				p.Icon = existing.Icon
				p.IconURL = existing.IconURL
				p.Description = existing.Description
				p.Downloads = existing.Downloads
				p.Categories = copyStringSlice(existing.Categories)
				p.CachedAt = existing.CachedAt
			}
		} else if isFullMetadata {
			p.CachedAt = time.Now().Unix()
		}
		state.ModPlatforms[key] = internModProject(pool, p)
		return nil
	})
	if err != nil {
		logging.Error("upsert mod platform failed", "platform", p.Platform, "projectID", p.ProjectID, "slug", p.Slug, "error", err)
		return err
	}
	logging.Info("mod platform upserted", "platform", p.Platform, "projectID", p.ProjectID, "slug", p.Slug)
	return nil
}

func GetModPlatform(platform, projectID string) (models.ModProject, bool) {
	d, err := readyDB()
	if err != nil {
		return models.ModProject{}, false
	}
	key := makePlatformKey(platform, projectID)
	if key.Platform == "" || key.ProjectID == "" {
		return models.ModProject{}, false
	}

	var p models.ModProject
	found := false
	err = d.view(func(state *cacheState) error {
		p, found = state.ModPlatforms[key]
		return nil
	})
	if err != nil || !found {
		logging.Debug("mod platform cache miss", "platform", key.Platform, "projectID", key.ProjectID)
		return models.ModProject{}, false
	}
	logging.Debug("mod platform cache hit", "platform", key.Platform, "projectID", key.ProjectID, "updatedAt", p.UpdatedAt)
	return copyProject(p), true
}

func GetModPlatformBySlug(platform, slug string) (models.ModProject, bool) {
	d, err := readyDB()
	if err != nil {
		return models.ModProject{}, false
	}
	platform = normalizePlatform(platform)
	slug = strings.TrimSpace(slug)
	if platform == "" || slug == "" {
		return models.ModProject{}, false
	}

	var p models.ModProject
	found := false
	err = d.view(func(state *cacheState) error {
		for key, candidate := range state.ModPlatforms {
			if key.Platform == platform && candidate.Slug == slug {
				p = candidate
				found = true
				break
			}
		}
		return nil
	})
	if err != nil || !found {
		logging.Debug("mod platform slug cache miss", "platform", platform, "slug", slug)
		return models.ModProject{}, false
	}
	logging.Debug("mod platform slug cache hit", "platform", platform, "slug", slug, "projectID", p.ProjectID, "updatedAt", p.UpdatedAt)
	return copyProject(p), true
}

func TouchModPlatform(platform, projectID string, updatedAt int64) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	key := makePlatformKey(platform, projectID)
	if key.Platform == "" || key.ProjectID == "" {
		return nil
	}

	err = d.update(func(state *cacheState, pool *stringPool) error {
		key := internPlatformKey(pool, key)
		p := state.ModPlatforms[key]
		p.Platform = key.Platform
		p.ProjectID = key.ProjectID
		p.UpdatedAt = updatedAt
		state.ModPlatforms[key] = internModProject(pool, p)
		return nil
	})
	if err != nil {
		logging.Error("touch mod platform failed", "platform", key.Platform, "projectID", key.ProjectID, "updatedAt", updatedAt, "error", err)
		return err
	}
	logging.Debug("mod platform touched", "platform", key.Platform, "projectID", key.ProjectID, "updatedAt", updatedAt)
	return nil
}

// --- platform associations ---

func UpsertPlatformAssociation(a PlatformAssociation) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	a.CurseForgeProjectID = strings.TrimSpace(a.CurseForgeProjectID)
	a.ModrinthProjectID = strings.TrimSpace(a.ModrinthProjectID)
	if a.ID == "" {
		a.ID = NewID()
	}

	err = d.update(func(state *cacheState, pool *stringPool) error {
		a = internPlatformAssociation(pool, a)
		state.PlatformAssociations[a.ID] = a
		return nil
	})
	if err != nil {
		logging.Error("upsert platform association failed", "id", a.ID, "curseforgeProjectID", a.CurseForgeProjectID, "modrinthProjectID", a.ModrinthProjectID, "error", err)
		return err
	}
	logging.Info("platform association upserted", "id", a.ID, "curseforgeProjectID", a.CurseForgeProjectID, "modrinthProjectID", a.ModrinthProjectID)
	return nil
}

func UpsertPlatformAssociationByProjects(curseForgeProjectID, modrinthProjectID string) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	curseForgeProjectID = strings.TrimSpace(curseForgeProjectID)
	modrinthProjectID = strings.TrimSpace(modrinthProjectID)
	if curseForgeProjectID == "" || modrinthProjectID == "" {
		return nil
	}

	err = d.update(func(state *cacheState, pool *stringPool) error {
		ids := make([]string, 0, 2)
		for _, a := range state.PlatformAssociations {
			if a.CurseForgeProjectID == curseForgeProjectID || a.ModrinthProjectID == modrinthProjectID {
				ids = append(ids, a.ID)
			}
		}
		id := NewID()
		if len(ids) > 0 {
			id = ids[0]
			for _, duplicateID := range ids[1:] {
				delete(state.PlatformAssociations, duplicateID)
			}
		}
		state.PlatformAssociations[id] = internPlatformAssociation(pool, PlatformAssociation{
			ID:                  id,
			CurseForgeProjectID: curseForgeProjectID,
			ModrinthProjectID:   modrinthProjectID,
		})
		return nil
	})
	if err != nil {
		logging.Error("upsert platform association by projects failed", "curseforgeProjectID", curseForgeProjectID, "modrinthProjectID", modrinthProjectID, "error", err)
		return err
	}
	logging.Info("platform association upserted by projects", "curseforgeProjectID", curseForgeProjectID, "modrinthProjectID", modrinthProjectID)
	return nil
}

func GetAssociationByCurseForge(cfProjectID string) (PlatformAssociation, bool) {
	cfProjectID = strings.TrimSpace(cfProjectID)
	return getAssociationBy(func(a PlatformAssociation) bool {
		return a.CurseForgeProjectID == cfProjectID
	}, "curseforgeProjectID", cfProjectID)
}

func GetAssociationByModrinth(mrProjectID string) (PlatformAssociation, bool) {
	mrProjectID = strings.TrimSpace(mrProjectID)
	return getAssociationBy(func(a PlatformAssociation) bool {
		return a.ModrinthProjectID == mrProjectID
	}, "modrinthProjectID", mrProjectID)
}

func getAssociationBy(match func(PlatformAssociation) bool, logKey string, logValue string) (PlatformAssociation, bool) {
	d, err := readyDB()
	if err != nil || logValue == "" {
		return PlatformAssociation{}, false
	}
	var out PlatformAssociation
	found := false
	err = d.view(func(state *cacheState) error {
		for _, a := range state.PlatformAssociations {
			if match(a) {
				out = a
				found = true
				break
			}
		}
		return nil
	})
	if err != nil || !found {
		logging.Debug("platform association miss", logKey, logValue)
		return PlatformAssociation{}, false
	}
	logging.Debug("platform association hit", logKey, logValue, "id", out.ID)
	return out, true
}

func GetLatestProjectBySHA1(platform, sha1 string) (models.ModVersion, bool) {
	d, err := readyDB()
	if err != nil {
		return models.ModVersion{}, false
	}
	platform = normalizePlatform(platform)
	sha1 = strings.ToLower(strings.TrimSpace(sha1))
	if platform == "" || sha1 == "" {
		return models.ModVersion{}, false
	}

	var best models.ModVersion
	found := false
	err = d.view(func(state *cacheState) error {
		latestByProject := make(map[string]models.ModVersion)
		for key, v := range state.PlatformVersions {
			if key.Platform != platform {
				continue
			}
			if current, ok := latestByProject[v.ProjectID]; !ok || newerProjectVersion(v, current) {
				latestByProject[v.ProjectID] = v
			}
		}
		for _, v := range latestByProject {
			if strings.ToLower(strings.TrimSpace(v.SHA1)) != sha1 {
				continue
			}
			if !found || v.PublishedAt > best.PublishedAt || (v.PublishedAt == best.PublishedAt && v.ProjectID < best.ProjectID) {
				best = copyVersion(v)
				found = true
			}
		}
		return nil
	})
	if err != nil || !found {
		return models.ModVersion{}, false
	}
	return best, true
}

func newerProjectVersion(left, right models.ModVersion) bool {
	if left.PublishedAt != right.PublishedAt {
		return left.PublishedAt > right.PublishedAt
	}
	return left.VersionID > right.VersionID
}

// GetVersionBySHA1 looks up any cached platform version whose SHA1 matches,
// regardless of whether it is the latest version for its project.
func GetVersionBySHA1(platform, sha1 string) (models.ModVersion, bool) {
	d, err := readyDB()
	if err != nil {
		return models.ModVersion{}, false
	}
	platform = normalizePlatform(platform)
	sha1 = strings.ToLower(strings.TrimSpace(sha1))
	if platform == "" || sha1 == "" {
		return models.ModVersion{}, false
	}

	var best models.ModVersion
	found := false
	err = d.view(func(state *cacheState) error {
		for key, v := range state.PlatformVersions {
			if key.Platform != platform {
				continue
			}
			if strings.ToLower(strings.TrimSpace(v.SHA1)) != sha1 {
				continue
			}
			if !found || v.PublishedAt > best.PublishedAt || (v.PublishedAt == best.PublishedAt && v.ProjectID < best.ProjectID) {
				best = copyVersion(v)
				found = true
			}
		}
		return nil
	})
	if err != nil || !found {
		return models.ModVersion{}, false
	}
	return best, true
}

// --- mod platform versions ---

func SetPlatformVersions(platform, projectID string, versions []models.ModVersion) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	platform = strings.TrimSpace(platform)
	projectID = strings.TrimSpace(projectID)
	if platform == "" || projectID == "" {
		return nil
	}

	logging.Debug("set platform versions started", "platform", platform, "projectID", projectID, "versionCount", len(versions))
	err = d.update(func(state *cacheState, pool *stringPool) error {
		for _, v := range versions {
			savePlatformVersion(state, pool, platform, projectID, v)
		}
		return nil
	})
	if err != nil {
		logging.Error("set platform versions failed", "platform", platform, "projectID", projectID, "versionCount", len(versions), "error", err)
		return err
	}
	logging.Info("platform versions set", "platform", platform, "projectID", projectID, "versionCount", len(versions))
	return nil
}

func SetPlatformVersionSnapshot(platform, projectID string, versions []models.ModVersion, updatedAt int64, scopes []ModPlatformVersionScope) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	platform = strings.TrimSpace(platform)
	projectID = strings.TrimSpace(projectID)
	if platform == "" || projectID == "" {
		return nil
	}
	scopes = normalizePlatformVersionScopes(scopes)

	logging.Debug("set platform version snapshot started", "platform", platform, "projectID", projectID, "versionCount", len(versions), "updatedAt", updatedAt, "scopeCount", len(scopes))
	err = d.update(func(state *cacheState, pool *stringPool) error {
		touchSnapshotPlatform(state, pool, platform, projectID, updatedAt, len(scopes) == 0)
		for _, v := range versions {
			savePlatformVersion(state, pool, platform, projectID, v)
		}
		for _, scope := range scopes {
			key := internVersionScopeKey(pool, makeVersionScopeKey(platform, projectID, scope))
			state.PlatformVersionScopes[key] = internStoredVersionScope(pool, storedVersionScope{
				Platform:         platform,
				ProjectID:        projectID,
				MinecraftVersion: scope.MinecraftVersion,
				ModLoader:        scope.ModLoader,
				UpdatedAt:        updatedAt,
			})
		}
		return nil
	})
	if err != nil {
		logging.Error("set platform version snapshot failed", "platform", platform, "projectID", projectID, "versionCount", len(versions), "error", err)
		return err
	}
	logging.Info("platform version snapshot set", "platform", platform, "projectID", projectID, "versionCount", len(versions), "updatedAt", updatedAt)
	return nil
}

func touchSnapshotPlatform(state *cacheState, pool *stringPool, platform, projectID string, updatedAt int64, updateProjectTimestamp bool) {
	key := internPlatformKey(pool, makePlatformKey(platform, projectID))
	p := state.ModPlatforms[key]
	if strings.TrimSpace(p.Platform) == "" {
		p.Platform = key.Platform
	}
	if strings.TrimSpace(p.ProjectID) == "" {
		p.ProjectID = key.ProjectID
	}
	if updateProjectTimestamp {
		p.UpdatedAt = updatedAt
	}
	state.ModPlatforms[key] = internModProject(pool, p)
}

func normalizePlatformVersionScopes(scopes []ModPlatformVersionScope) []ModPlatformVersionScope {
	seen := make(map[string]struct{}, len(scopes))
	out := make([]ModPlatformVersionScope, 0, len(scopes))
	for _, scope := range scopes {
		scope.MinecraftVersion = strings.TrimSpace(scope.MinecraftVersion)
		scope.ModLoader = strings.ToLower(strings.TrimSpace(scope.ModLoader))
		if scope.MinecraftVersion == "" && scope.ModLoader == "" {
			continue
		}
		key := scope.MinecraftVersion + "|" + scope.ModLoader
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func savePlatformVersion(state *cacheState, pool *stringPool, platform, projectID string, v models.ModVersion) {
	v = copyVersion(v)
	v.Platform = strings.TrimSpace(platform)
	v.ProjectID = projectID
	v.VersionID = strings.TrimSpace(v.VersionID)
	key := internVersionKey(pool, makeVersionKey(v.Platform, projectID, v.VersionID))

	if existing, ok := state.PlatformVersions[key]; ok && existing.ID != "" {
		v.ID = existing.ID
	}
	if v.ID == "" {
		v.ID = NewID()
	}
	v.Dependencies = normalizeDependencies(v.ID, v.Dependencies)
	v = internModVersion(pool, v)
	state.PlatformVersions[key] = v
	state.PlatformVersionKeyByID[v.ID] = key
}

func normalizeDependencies(platformVersionID string, deps []models.ModDependency) []models.ModDependency {
	out := make([]models.ModDependency, 0, len(deps))
	for _, dep := range deps {
		projectID := strings.TrimSpace(dep.DependencyProjectID)
		versionID := strings.TrimSpace(dep.DependencyVersionID)
		if projectID == "" && versionID == "" {
			continue
		}
		if dep.ID == "" {
			dep.ID = NewID()
		}
		dep.PlatformVersionID = platformVersionID
		dep.DependencyProjectID = projectID
		dep.DependencyVersionID = versionID
		dep.DependencyType = strings.TrimSpace(dep.DependencyType)
		out = append(out, dep)
	}
	return out
}

func GetPlatformVersionScopeUpdatedAt(platform, projectID string, scope ModPlatformVersionScope) (int64, bool) {
	d, err := readyDB()
	if err != nil {
		return 0, false
	}
	platform = normalizePlatform(platform)
	projectID = strings.TrimSpace(projectID)
	scopes := normalizePlatformVersionScopes([]ModPlatformVersionScope{scope})
	if platform == "" || projectID == "" || len(scopes) == 0 {
		return 0, false
	}
	key := makeVersionScopeKey(platform, projectID, scopes[0])

	var rec storedVersionScope
	found := false
	err = d.view(func(state *cacheState) error {
		rec, found = state.PlatformVersionScopes[key]
		return nil
	})
	if err != nil || !found {
		return 0, false
	}
	return rec.UpdatedAt, true
}

func GetPlatformVersions(platform, projectID string) ([]models.ModVersion, error) {
	d, err := readyDB()
	if err != nil {
		return nil, err
	}
	platform = normalizePlatform(platform)
	projectID = strings.TrimSpace(projectID)
	if platform == "" || projectID == "" {
		return nil, nil
	}

	versions := make([]models.ModVersion, 0)
	err = d.view(func(state *cacheState) error {
		for key, v := range state.PlatformVersions {
			if key.Platform == platform && key.ProjectID == projectID {
				versions = append(versions, copyVersion(v))
			}
		}
		sort.Slice(versions, func(i, j int) bool {
			return versions[i].VersionID < versions[j].VersionID
		})
		return nil
	})
	if err != nil {
		logging.Error("get platform versions failed", "platform", platform, "projectID", projectID, "error", err)
		return nil, err
	}
	logging.Debug("platform versions loaded", "platform", platform, "projectID", projectID, "versionCount", len(versions))
	return versions, nil
}

// --- mod dependencies ---

func SetVersionDependencies(platformVersionID string, deps []models.ModDependency) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	platformVersionID = strings.TrimSpace(platformVersionID)
	if platformVersionID == "" {
		return nil
	}

	logging.Debug("set version dependencies started", "platformVersionID", platformVersionID, "dependencyCount", len(deps))
	err = d.update(func(state *cacheState, pool *stringPool) error {
		key, ok := state.PlatformVersionKeyByID[platformVersionID]
		if !ok {
			return nil
		}
		v, ok := state.PlatformVersions[key]
		if !ok {
			return nil
		}
		v.Dependencies = internDependencies(pool, normalizeDependencies(platformVersionID, deps))
		state.PlatformVersions[key] = v
		return nil
	})
	if err != nil {
		logging.Error("set version dependencies failed", "platformVersionID", platformVersionID, "dependencyCount", len(deps), "error", err)
		return err
	}
	logging.Info("version dependencies set", "platformVersionID", platformVersionID, "dependencyCount", len(deps))
	return nil
}

func GetVersionDependencies(platformVersionID string) ([]models.ModDependency, error) {
	d, err := readyDB()
	if err != nil {
		return nil, err
	}
	platformVersionID = strings.TrimSpace(platformVersionID)
	if platformVersionID == "" {
		return nil, nil
	}

	var deps []models.ModDependency
	err = d.view(func(state *cacheState) error {
		key, ok := state.PlatformVersionKeyByID[platformVersionID]
		if !ok {
			return nil
		}
		if v, ok := state.PlatformVersions[key]; ok {
			deps = copyDependencies(v.Dependencies)
		}
		return nil
	})
	if err != nil {
		logging.Error("get version dependencies failed", "platformVersionID", platformVersionID, "error", err)
		return nil, err
	}
	logging.Debug("version dependencies loaded", "platformVersionID", platformVersionID, "dependencyCount", len(deps))
	return deps, nil
}

// --- pinned mods ---

func UpsertPinnedMod(p PinnedMod) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	p = normalizePinnedMod(p)
	if p.Platform == "" || p.ModID == "" || p.MinecraftVersion == "" || p.ModLoader == "" {
		return nil
	}

	err = d.update(func(state *cacheState, pool *stringPool) error {
		key := internPinnedModKey(pool, makePinnedModKey(p.Platform, p.ModID, p.MinecraftVersion, p.ModLoader))
		if existing, ok := state.PinnedMods[key]; ok && existing.ID != "" {
			p.ID = existing.ID
		}
		if p.ID == "" {
			p.ID = NewID()
		}
		state.PinnedMods[key] = internPinnedMod(pool, p)
		return nil
	})
	if err != nil {
		logging.Error("upsert pinned mod failed", "platform", p.Platform, "modID", p.ModID, "versionID", p.VersionID, "minecraftVersion", p.MinecraftVersion, "modLoader", p.ModLoader, "error", err)
		return err
	}
	logging.Info("pinned mod upserted", "platform", p.Platform, "modID", p.ModID, "versionID", p.VersionID, "minecraftVersion", p.MinecraftVersion, "modLoader", p.ModLoader)
	return nil
}

func GetPinnedMod(platform, modID, mcVersion, modLoader string) (PinnedMod, bool) {
	d, err := readyDB()
	if err != nil {
		return PinnedMod{}, false
	}
	key := makePinnedModKey(platform, modID, mcVersion, modLoader)
	if key.Platform == "" || key.ModID == "" || key.MinecraftVersion == "" || key.ModLoader == "" {
		return PinnedMod{}, false
	}

	var p PinnedMod
	found := false
	err = d.view(func(state *cacheState) error {
		p, found = state.PinnedMods[key]
		return nil
	})
	if err != nil || !found {
		logging.Debug("pinned mod miss", "platform", key.Platform, "modID", key.ModID, "minecraftVersion", key.MinecraftVersion, "modLoader", key.ModLoader)
		return PinnedMod{}, false
	}
	logging.Debug("pinned mod hit", "platform", key.Platform, "modID", key.ModID, "versionID", p.VersionID, "minecraftVersion", key.MinecraftVersion, "modLoader", key.ModLoader)
	return p, true
}

func DeletePinnedMod(platform, modID, mcVersion, modLoader string) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	key := makePinnedModKey(platform, modID, mcVersion, modLoader)
	if key.Platform == "" || key.ModID == "" || key.MinecraftVersion == "" || key.ModLoader == "" {
		return nil
	}

	err = d.update(func(state *cacheState, pool *stringPool) error {
		delete(state.PinnedMods, key)
		return nil
	})
	if err != nil {
		logging.Error("delete pinned mod failed", "platform", key.Platform, "modID", key.ModID, "minecraftVersion", key.MinecraftVersion, "modLoader", key.ModLoader, "error", err)
		return err
	}
	logging.Info("pinned mod deleted", "platform", key.Platform, "modID", key.ModID, "minecraftVersion", key.MinecraftVersion, "modLoader", key.ModLoader)
	return nil
}

// ListPinnedMods returns a snapshot of all pinned mods, sorted in ascending stable order by Platform -> ModID -> MinecraftVersion -> ModLoader.
// The returned value is a copy; callers may safely modify it.
func ListPinnedMods() []PinnedMod {
	d, err := readyDB()
	if err != nil {
		return nil
	}

	var pins []PinnedMod
	err = d.view(func(state *cacheState) error {
		pins = make([]PinnedMod, 0, len(state.PinnedMods))
		for _, p := range state.PinnedMods {
			pins = append(pins, p)
		}
		return nil
	})
	if err != nil {
		logging.Error("list pinned mods failed", "error", err)
		return nil
	}

	sort.SliceStable(pins, func(i, j int) bool {
		if pins[i].Platform != pins[j].Platform {
			return pins[i].Platform < pins[j].Platform
		}
		if pins[i].ModID != pins[j].ModID {
			return pins[i].ModID < pins[j].ModID
		}
		if pins[i].MinecraftVersion != pins[j].MinecraftVersion {
			return pins[i].MinecraftVersion < pins[j].MinecraftVersion
		}
		return pins[i].ModLoader < pins[j].ModLoader
	})
	return pins
}

// --- favorite lists ---

func CreateFavoriteList(name string) (FavoriteList, error) {
	d, err := readyDB()
	if err != nil {
		return FavoriteList{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return FavoriteList{}, nil
	}

	var list FavoriteList
	err = d.update(func(state *cacheState, pool *stringPool) error {
		now := time.Now().Unix()
		maxSortOrder := 0
		for _, existing := range state.FavoriteLists {
			if existing.SortOrder > maxSortOrder {
				maxSortOrder = existing.SortOrder
			}
		}
		list = FavoriteList{
			ID:        NewID(),
			Name:      name,
			CreatedAt: now,
			UpdatedAt: now,
			SortOrder: maxSortOrder + 1,
		}
		list = internFavoriteList(pool, list)
		state.FavoriteLists[list.ID] = list
		return nil
	})
	if err != nil {
		logging.Error("create favorite list failed", "name", name, "error", err)
		return FavoriteList{}, err
	}
	logging.Info("favorite list created", "id", list.ID, "name", list.Name)
	return list, nil
}

func UpdateFavoriteListName(id, name string) (FavoriteList, bool, error) {
	d, err := readyDB()
	if err != nil {
		return FavoriteList{}, false, err
	}
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" || name == "" {
		return FavoriteList{}, false, nil
	}

	var list FavoriteList
	found := false
	err = d.update(func(state *cacheState, pool *stringPool) error {
		existing, ok := state.FavoriteLists[id]
		if !ok {
			return nil
		}
		existing.Name = name
		existing.UpdatedAt = time.Now().Unix()
		list = internFavoriteList(pool, normalizeFavoriteList(existing))
		state.FavoriteLists[list.ID] = list
		found = true
		return nil
	})
	if err != nil {
		logging.Error("rename favorite list failed", "id", id, "name", name, "error", err)
		return FavoriteList{}, false, err
	}
	if found {
		logging.Info("favorite list renamed", "id", id, "name", name)
	}
	return list, found, nil
}

func DeleteFavoriteList(id string) (bool, error) {
	d, err := readyDB()
	if err != nil {
		return false, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}

	deleted := false
	err = d.update(func(state *cacheState, pool *stringPool) error {
		if _, ok := state.FavoriteLists[id]; !ok {
			return nil
		}
		delete(state.FavoriteLists, id)
		for key := range state.FavoriteMods {
			if key.ListID == id {
				delete(state.FavoriteMods, key)
			}
		}
		deleted = true
		return nil
	})
	if err != nil {
		logging.Error("delete favorite list failed", "id", id, "error", err)
		return false, err
	}
	if deleted {
		logging.Info("favorite list deleted", "id", id)
	}
	return deleted, nil
}

func ListFavoriteLists() []FavoriteList {
	d, err := readyDB()
	if err != nil {
		return nil
	}

	var lists []FavoriteList
	err = d.view(func(state *cacheState) error {
		lists = make([]FavoriteList, 0, len(state.FavoriteLists))
		for _, list := range state.FavoriteLists {
			lists = append(lists, list)
		}
		return nil
	})
	if err != nil {
		logging.Error("list favorite lists failed", "error", err)
		return nil
	}
	sort.SliceStable(lists, func(i, j int) bool {
		if lists[i].SortOrder != lists[j].SortOrder {
			return lists[i].SortOrder < lists[j].SortOrder
		}
		if lists[i].CreatedAt != lists[j].CreatedAt {
			return lists[i].CreatedAt < lists[j].CreatedAt
		}
		return lists[i].Name < lists[j].Name
	})
	return lists
}

func UpsertFavoriteMod(mod FavoriteMod) (FavoriteMod, bool, error) {
	d, err := readyDB()
	if err != nil {
		return FavoriteMod{}, false, err
	}
	mod = normalizeFavoriteMod(mod)
	if mod.ListID == "" || mod.Platform == "" || mod.ModID == "" {
		return FavoriteMod{}, false, nil
	}

	var saved FavoriteMod
	ok := false
	err = d.update(func(state *cacheState, pool *stringPool) error {
		if _, foundList := state.FavoriteLists[mod.ListID]; !foundList {
			return nil
		}
		now := time.Now().Unix()
		key := internFavoriteModKey(pool, makeFavoriteModKey(mod.ListID, mod.Platform, mod.ModID, mod.MinecraftVersion, mod.ModLoader))
		if existing, found := state.FavoriteMods[key]; found {
			if mod.ID == "" {
				mod.ID = existing.ID
			}
			if mod.CreatedAt == 0 {
				mod.CreatedAt = existing.CreatedAt
			}
		}
		if mod.ID == "" {
			mod.ID = NewID()
		}
		if mod.CreatedAt == 0 {
			mod.CreatedAt = now
		}
		mod.UpdatedAt = now
		saved = internFavoriteMod(pool, mod)
		state.FavoriteMods[key] = saved
		ok = true
		return nil
	})
	if err != nil {
		logging.Error("upsert favorite mod failed", "listID", mod.ListID, "platform", mod.Platform, "modID", mod.ModID, "error", err)
		return FavoriteMod{}, false, err
	}
	if ok {
		logging.Info("favorite mod upserted", "listID", saved.ListID, "platform", saved.Platform, "modID", saved.ModID)
	}
	return copyFavoriteMod(saved), ok, nil
}

func DeleteFavoriteMod(listID, platform, modID, mcVersion, modLoader string) (bool, error) {
	d, err := readyDB()
	if err != nil {
		return false, err
	}
	key := makeFavoriteModKey(listID, platform, modID, mcVersion, modLoader)
	if key.ListID == "" || key.Platform == "" || key.ModID == "" {
		return false, nil
	}

	deleted := false
	err = d.update(func(state *cacheState, pool *stringPool) error {
		if _, ok := state.FavoriteMods[key]; ok {
			delete(state.FavoriteMods, key)
			deleted = true
		}
		return nil
	})
	if err != nil {
		logging.Error("delete favorite mod failed", "listID", key.ListID, "platform", key.Platform, "modID", key.ModID, "error", err)
		return false, err
	}
	if deleted {
		logging.Info("favorite mod deleted", "listID", key.ListID, "platform", key.Platform, "modID", key.ModID)
	}
	return deleted, nil
}

func ListFavoriteMods(listID string) []FavoriteMod {
	d, err := readyDB()
	if err != nil {
		return nil
	}
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return nil
	}

	var mods []FavoriteMod
	err = d.view(func(state *cacheState) error {
		mods = make([]FavoriteMod, 0)
		for key, mod := range state.FavoriteMods {
			if key.ListID == listID {
				mods = append(mods, copyFavoriteMod(mod))
			}
		}
		return nil
	})
	if err != nil {
		logging.Error("list favorite mods failed", "listID", listID, "error", err)
		return nil
	}
	sort.SliceStable(mods, func(i, j int) bool {
		if mods[i].CreatedAt != mods[j].CreatedAt {
			return mods[i].CreatedAt < mods[j].CreatedAt
		}
		if mods[i].Platform != mods[j].Platform {
			return mods[i].Platform < mods[j].Platform
		}
		return mods[i].ModID < mods[j].ModID
	})
	return mods
}

// --- version mod IDs ---

func SetVersionModIDs(platformVersionID string, modIDs []string) error {
	d, err := readyDB()
	if err != nil {
		return err
	}
	platformVersionID = strings.TrimSpace(platformVersionID)
	if platformVersionID == "" {
		return nil
	}

	logging.Debug("set version mod IDs started", "platformVersionID", platformVersionID, "modIDCount", len(modIDs))
	err = d.update(func(state *cacheState, pool *stringPool) error {
		key, ok := state.PlatformVersionKeyByID[platformVersionID]
		if !ok {
			return nil
		}
		v, ok := state.PlatformVersions[key]
		if !ok {
			return nil
		}
		seen := make(map[string]struct{}, len(modIDs))
		out := make([]string, 0, len(modIDs))
		for _, id := range modIDs {
			id = strings.ToLower(strings.TrimSpace(id))
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, pool.Intern(id))
		}
		v.ModIDs = out
		state.PlatformVersions[key] = v
		return nil
	})
	if err != nil {
		logging.Error("set version mod IDs failed", "platformVersionID", platformVersionID, "modIDCount", len(modIDs), "error", err)
		return err
	}
	logging.Info("version mod IDs set", "platformVersionID", platformVersionID, "modIDCount", len(modIDs))
	return nil
}

// GetVersionModIDs returns the persisted mod IDs for a platform version ID.
// Returns nil (no error) when the version is unknown or has no persisted IDs;
// callers should treat nil as a cache miss.
func GetVersionModIDs(platformVersionID string) ([]string, error) {
	d, err := readyDB()
	if err != nil {
		return nil, err
	}
	platformVersionID = strings.TrimSpace(platformVersionID)
	if platformVersionID == "" {
		return nil, nil
	}

	var modIDs []string
	err = d.view(func(state *cacheState) error {
		key, ok := state.PlatformVersionKeyByID[platformVersionID]
		if !ok {
			return nil
		}
		if v, ok := state.PlatformVersions[key]; ok {
			modIDs = copyStringSlice(v.ModIDs)
		}
		return nil
	})
	if err != nil {
		logging.Error("get version mod IDs failed", "platformVersionID", platformVersionID, "error", err)
		return nil, err
	}
	logging.Debug("version mod IDs loaded", "platformVersionID", platformVersionID, "modIDCount", len(modIDs))
	return modIDs, nil
}
