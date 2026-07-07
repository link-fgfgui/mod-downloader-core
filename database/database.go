package database

import (
	"crypto/rand"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/link-fgfgui/mod-downloader-core/logging"
	"github.com/link-fgfgui/mod-downloader-core/models"

	"github.com/klauspost/compress/zstd"
)

const (
	CacheFileName    = "mods.gob.zst"
	UserDataFileName = "user-data.sqlite"
	databaseFileName = CacheFileName
	cacheVersion     = 4
)

var (
	db         *cacheDB
	dbPath     string
	userDB     *userStore
	userDBPath string
)

var errDatabaseNotOpen = errors.New("database is not open")
var errUserDatabaseNotOpen = errors.New("user database is not open")

type cacheDB struct {
	mu      sync.RWMutex
	path    string
	state   cacheState
	strings stringPool
}

type cacheState struct {
	Version               int
	ModPlatforms          map[platformKey]models.ModProject
	PlatformAssociations  map[string]PlatformAssociation
	PlatformVersions      map[versionKey]models.ModVersion
	PlatformVersionScopes map[versionScopeKey]storedVersionScope
	// Legacy user-owned records are decoded only for one-time SQLite migration.
	PinnedMods             map[pinnedModKey]PinnedMod
	FavoriteLists          map[string]FavoriteList
	FavoriteMods           map[favoriteModKey]FavoriteMod
	PlatformVersionKeyByID map[string]versionKey
}

type platformKey struct {
	Platform  string
	ProjectID string
}

type versionKey struct {
	Platform  string
	ProjectID string
	VersionID string
}

type versionScopeKey struct {
	Platform         string
	ProjectID        string
	MinecraftVersion string
	ModLoader        string
}

type pinnedModKey struct {
	Platform         string
	ModID            string
	MinecraftVersion string
	ModLoader        string
}

type favoriteModKey struct {
	ListID           string
	Platform         string
	ModID            string
	MinecraftVersion string
	ModLoader        string
}

type stringPool struct {
	values map[string]string
}

func newStringPool() stringPool {
	return stringPool{values: make(map[string]string)}
}

func (p *stringPool) Intern(s string) string {
	if s == "" {
		return ""
	}
	if v, ok := p.values[s]; ok {
		return v
	}
	p.values[s] = s
	return s
}

func Open() error {
	targetPath, err := DefaultCachePath()
	if err != nil {
		return err
	}
	return OpenAt(targetPath)
}

func DefaultCachePath() (string, error) {
	dir := os.TempDir()
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("empty temp dir")
	}
	return filepath.Join(dir, "mod-downloader", databaseFileName), nil
}

func UserDataPathForCachePath(cachePath string) string {
	return filepath.Join(filepath.Dir(cachePath), UserDataFileName)
}

func OpenAt(path string) error {
	targetPath := strings.TrimSpace(path)
	if targetPath == "" {
		return errors.New("empty cache path")
	}
	if !filepath.IsAbs(targetPath) {
		abs, err := filepath.Abs(targetPath)
		if err != nil {
			return fmt.Errorf("resolve cache path: %w", err)
		}
		targetPath = abs
	}
	targetPath = filepath.Clean(targetPath)
	targetUserPath := UserDataPathForCachePath(targetPath)
	if db != nil && dbPath == targetPath && userDB != nil && userDBPath == targetUserPath {
		logging.Debug("cache already open", "path", targetPath)
		return nil
	}

	if db != nil {
		logging.Info("cache path changed, closing previous store", "previousPath", dbPath, "nextPath", targetPath)
		Close()
	}

	logging.Info("opening cache", "path", targetPath)
	state, err := loadCacheState(targetPath)
	if err != nil {
		logging.Error("open cache failed", "path", targetPath, "error", err)
		return fmt.Errorf("open cache: %w", err)
	}
	state.normalize()
	nextUserDB, err := openUserStore(targetUserPath)
	if err != nil {
		logging.Error("open user database failed", "path", targetUserPath, "error", err)
		return fmt.Errorf("open user database: %w", err)
	}
	if err := nextUserDB.migrateLegacyUserData(&state); err != nil {
		_ = nextUserDB.close()
		logging.Error("migrate legacy user data failed", "path", targetUserPath, "error", err)
		return fmt.Errorf("migrate legacy user data: %w", err)
	}
	pool := newStringPool()
	state.intern(&pool)
	nextDB := &cacheDB{path: targetPath, state: state, strings: pool}

	db = nextDB
	dbPath = targetPath
	userDB = nextUserDB
	userDBPath = targetUserPath
	logging.Info("cache opened", "path", targetPath)
	return nil
}

func Close() {
	if db != nil {
		logging.Info("closing cache", "path", dbPath)
		if err := db.save(); err != nil {
			logging.Warn("save cache failed", "path", dbPath, "error", err)
		}
		db = nil
	}
	dbPath = ""
	if userDB != nil {
		logging.Info("closing user database", "path", userDBPath)
		if err := userDB.close(); err != nil {
			logging.Warn("close user database failed", "path", userDBPath, "error", err)
		}
		userDB = nil
	}
	userDBPath = ""
}

func readyDB() (*cacheDB, error) {
	if db == nil {
		logging.Warn("cache access attempted before open")
		return nil, errDatabaseNotOpen
	}
	return db, nil
}

func readyUserDB() (*userStore, error) {
	if userDB == nil {
		logging.Warn("user database access attempted before open")
		return nil, errUserDatabaseNotOpen
	}
	return userDB, nil
}

func NewID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func loadCacheState(path string) (cacheState, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newCacheState(), nil
		}
		return cacheState{}, err
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return cacheState{}, err
	}
	defer zr.Close()

	var state cacheState
	if err := gob.NewDecoder(zr).Decode(&state); err != nil {
		return cacheState{}, err
	}
	if state.Version == 0 {
		state.Version = cacheVersion
	}
	if state.Version < cacheVersion {
		logging.Info("cache version outdated, discarding", "fileVersion", state.Version, "currentVersion", cacheVersion)
		return newCacheState(), nil
	}
	return state, nil
}

func newCacheState() cacheState {
	state := cacheState{Version: cacheVersion}
	state.normalize()
	return state
}

func (s *cacheState) normalize() {
	if s.Version == 0 {
		s.Version = cacheVersion
	}
	if s.ModPlatforms == nil {
		s.ModPlatforms = make(map[platformKey]models.ModProject)
	}
	if s.PlatformAssociations == nil {
		s.PlatformAssociations = make(map[string]PlatformAssociation)
	}
	if s.PlatformVersions == nil {
		s.PlatformVersions = make(map[versionKey]models.ModVersion)
	}
	if s.PlatformVersionScopes == nil {
		s.PlatformVersionScopes = make(map[versionScopeKey]storedVersionScope)
	}
	if s.PinnedMods == nil {
		s.PinnedMods = make(map[pinnedModKey]PinnedMod)
	}
	if s.FavoriteLists == nil {
		s.FavoriteLists = make(map[string]FavoriteList)
	}
	if s.FavoriteMods == nil {
		s.FavoriteMods = make(map[favoriteModKey]FavoriteMod)
	}
	if s.PlatformVersionKeyByID == nil {
		s.PlatformVersionKeyByID = make(map[string]versionKey)
	}
	s.normalizeKeys()
}

func (s *cacheState) normalizeKeys() {
	modPlatforms := make(map[platformKey]models.ModProject, len(s.ModPlatforms))
	for key, project := range s.ModPlatforms {
		key = makePlatformKey(key.Platform, key.ProjectID)
		if project.Platform == "" {
			project.Platform = key.Platform
		}
		if project.ProjectID == "" {
			project.ProjectID = key.ProjectID
		}
		modPlatforms[key] = project
	}
	s.ModPlatforms = modPlatforms

	versions := make(map[versionKey]models.ModVersion, len(s.PlatformVersions))
	versionKeyByID := make(map[string]versionKey, len(s.PlatformVersions))
	for key, version := range s.PlatformVersions {
		key = makeVersionKey(key.Platform, key.ProjectID, key.VersionID)
		if version.Platform == "" {
			version.Platform = key.Platform
		}
		if version.ProjectID == "" {
			version.ProjectID = key.ProjectID
		}
		if version.VersionID == "" {
			version.VersionID = key.VersionID
		}
		versions[key] = version
		if version.ID != "" {
			versionKeyByID[version.ID] = key
		}
	}
	s.PlatformVersions = versions
	s.PlatformVersionKeyByID = versionKeyByID

	scopes := make(map[versionScopeKey]storedVersionScope, len(s.PlatformVersionScopes))
	for key, scope := range s.PlatformVersionScopes {
		key = makeVersionScopeKey(key.Platform, key.ProjectID, ModPlatformVersionScope{
			MinecraftVersion: key.MinecraftVersion,
			ModLoader:        key.ModLoader,
		})
		if scope.Platform == "" {
			scope.Platform = key.Platform
		}
		if scope.ProjectID == "" {
			scope.ProjectID = key.ProjectID
		}
		if scope.MinecraftVersion == "" {
			scope.MinecraftVersion = key.MinecraftVersion
		}
		if scope.ModLoader == "" {
			scope.ModLoader = key.ModLoader
		}
		scopes[key] = scope
	}
	s.PlatformVersionScopes = scopes

	pinnedMods := make(map[pinnedModKey]PinnedMod, len(s.PinnedMods))
	for key, pin := range s.PinnedMods {
		key = makePinnedModKey(key.Platform, key.ModID, key.MinecraftVersion, key.ModLoader)
		pin = normalizePinnedMod(pin)
		if pin.ID == "" {
			pin.ID = key.Platform + ":" + key.ModID + ":" + key.MinecraftVersion + ":" + key.ModLoader
		}
		pinnedMods[key] = pin
	}
	s.PinnedMods = pinnedMods

	favoriteLists := make(map[string]FavoriteList, len(s.FavoriteLists))
	for id, list := range s.FavoriteLists {
		list = normalizeFavoriteList(list)
		if list.ID == "" {
			list.ID = strings.TrimSpace(id)
		}
		if list.ID == "" {
			continue
		}
		favoriteLists[list.ID] = list
	}
	s.FavoriteLists = favoriteLists

	favoriteMods := make(map[favoriteModKey]FavoriteMod, len(s.FavoriteMods))
	for key, mod := range s.FavoriteMods {
		if mod.ListID == "" {
			mod.ListID = key.ListID
		}
		if mod.Platform == "" {
			mod.Platform = key.Platform
		}
		if mod.ModID == "" {
			mod.ModID = key.ModID
		}
		if mod.MinecraftVersion == "" {
			mod.MinecraftVersion = key.MinecraftVersion
		}
		if mod.ModLoader == "" {
			mod.ModLoader = key.ModLoader
		}
		mod = normalizeFavoriteMod(mod)
		key = makeFavoriteModKey(mod.ListID, mod.Platform, mod.ModID, mod.MinecraftVersion, mod.ModLoader)
		if key.ListID == "" || key.Platform == "" || key.ModID == "" {
			continue
		}
		if mod.ID == "" {
			mod.ID = key.ListID + ":" + key.Platform + ":" + key.ModID + ":" + key.MinecraftVersion + ":" + key.ModLoader
		}
		favoriteMods[key] = mod
	}
	s.FavoriteMods = favoriteMods
}

func (s *cacheState) intern(pool *stringPool) {
	if pool == nil {
		return
	}
	modPlatforms := make(map[platformKey]models.ModProject, len(s.ModPlatforms))
	for key, project := range s.ModPlatforms {
		key = internPlatformKey(pool, key)
		project = internModProject(pool, project)
		modPlatforms[key] = project
	}
	s.ModPlatforms = modPlatforms

	associations := make(map[string]PlatformAssociation, len(s.PlatformAssociations))
	for id, association := range s.PlatformAssociations {
		association = internPlatformAssociation(pool, association)
		associations[id] = association
	}
	s.PlatformAssociations = associations

	versions := make(map[versionKey]models.ModVersion, len(s.PlatformVersions))
	for key, version := range s.PlatformVersions {
		key = internVersionKey(pool, key)
		version = internModVersion(pool, version)
		versions[key] = version
	}
	s.PlatformVersions = versions

	scopes := make(map[versionScopeKey]storedVersionScope, len(s.PlatformVersionScopes))
	for key, scope := range s.PlatformVersionScopes {
		key = internVersionScopeKey(pool, key)
		scope = internStoredVersionScope(pool, scope)
		scopes[key] = scope
	}
	s.PlatformVersionScopes = scopes

	pinnedMods := make(map[pinnedModKey]PinnedMod, len(s.PinnedMods))
	for key, pin := range s.PinnedMods {
		key = internPinnedModKey(pool, key)
		pin = internPinnedMod(pool, pin)
		pinnedMods[key] = pin
	}
	s.PinnedMods = pinnedMods

	favoriteLists := make(map[string]FavoriteList, len(s.FavoriteLists))
	for id, list := range s.FavoriteLists {
		id = pool.Intern(id)
		list = internFavoriteList(pool, list)
		favoriteLists[id] = list
	}
	s.FavoriteLists = favoriteLists

	favoriteMods := make(map[favoriteModKey]FavoriteMod, len(s.FavoriteMods))
	for key, mod := range s.FavoriteMods {
		key = internFavoriteModKey(pool, key)
		mod = internFavoriteMod(pool, mod)
		favoriteMods[key] = mod
	}
	s.FavoriteMods = favoriteMods

	versionKeyByID := make(map[string]versionKey, len(s.PlatformVersionKeyByID))
	for id, key := range s.PlatformVersionKeyByID {
		versionKeyByID[id] = internVersionKey(pool, key)
	}
	s.PlatformVersionKeyByID = versionKeyByID
}

func (s *cacheState) clearLegacyUserData() {
	s.PinnedMods = make(map[pinnedModKey]PinnedMod)
	s.FavoriteLists = make(map[string]FavoriteList)
	s.FavoriteMods = make(map[favoriteModKey]FavoriteMod)
}

func internPlatformKey(pool *stringPool, key platformKey) platformKey {
	key.Platform = pool.Intern(key.Platform)
	key.ProjectID = pool.Intern(key.ProjectID)
	return key
}

func internVersionKey(pool *stringPool, key versionKey) versionKey {
	key.Platform = pool.Intern(key.Platform)
	key.ProjectID = pool.Intern(key.ProjectID)
	return key
}

func internVersionScopeKey(pool *stringPool, key versionScopeKey) versionScopeKey {
	key.Platform = pool.Intern(key.Platform)
	key.ProjectID = pool.Intern(key.ProjectID)
	key.MinecraftVersion = pool.Intern(key.MinecraftVersion)
	key.ModLoader = pool.Intern(key.ModLoader)
	return key
}

func internPinnedModKey(pool *stringPool, key pinnedModKey) pinnedModKey {
	key.Platform = pool.Intern(key.Platform)
	key.ModID = pool.Intern(key.ModID)
	key.MinecraftVersion = pool.Intern(key.MinecraftVersion)
	key.ModLoader = pool.Intern(key.ModLoader)
	return key
}

func internFavoriteModKey(pool *stringPool, key favoriteModKey) favoriteModKey {
	key.ListID = pool.Intern(key.ListID)
	key.Platform = pool.Intern(key.Platform)
	key.ModID = pool.Intern(key.ModID)
	key.MinecraftVersion = pool.Intern(key.MinecraftVersion)
	key.ModLoader = pool.Intern(key.ModLoader)
	return key
}

func internStringSlice(pool *stringPool, values []string) []string {
	for i, value := range values {
		values[i] = pool.Intern(value)
	}
	return values
}

func internModProject(pool *stringPool, project models.ModProject) models.ModProject {
	project.Platform = pool.Intern(project.Platform)
	project.ProjectID = pool.Intern(project.ProjectID)
	project.Slug = pool.Intern(project.Slug)
	project.Icon = pool.Intern(project.Icon)
	project.Categories = internStringSlice(pool, project.Categories)
	return project
}

func internPlatformAssociation(pool *stringPool, association PlatformAssociation) PlatformAssociation {
	association.CurseForgeProjectID = pool.Intern(association.CurseForgeProjectID)
	association.ModrinthProjectID = pool.Intern(association.ModrinthProjectID)
	return association
}

func internModVersion(pool *stringPool, version models.ModVersion) models.ModVersion {
	version.Platform = pool.Intern(version.Platform)
	version.ProjectID = pool.Intern(version.ProjectID)
	version.GameVersions = internStringSlice(pool, version.GameVersions)
	version.Loaders = internStringSlice(pool, version.Loaders)
	version.Dependencies = internDependencies(pool, version.Dependencies)
	version.ModIDs = internStringSlice(pool, version.ModIDs)
	return version
}

func internDependencies(pool *stringPool, deps []models.ModDependency) []models.ModDependency {
	for i := range deps {
		deps[i].DependencyProjectID = pool.Intern(deps[i].DependencyProjectID)
		deps[i].DependencyType = pool.Intern(deps[i].DependencyType)
	}
	return deps
}

func internStoredVersionScope(pool *stringPool, scope storedVersionScope) storedVersionScope {
	scope.Platform = pool.Intern(scope.Platform)
	scope.ProjectID = pool.Intern(scope.ProjectID)
	scope.MinecraftVersion = pool.Intern(scope.MinecraftVersion)
	scope.ModLoader = pool.Intern(scope.ModLoader)
	return scope
}

func internPinnedMod(pool *stringPool, pin PinnedMod) PinnedMod {
	pin.ID = pool.Intern(pin.ID)
	pin.Platform = pool.Intern(pin.Platform)
	pin.ModID = pool.Intern(pin.ModID)
	pin.VersionID = pool.Intern(pin.VersionID)
	pin.MinecraftVersion = pool.Intern(pin.MinecraftVersion)
	pin.ModLoader = pool.Intern(pin.ModLoader)
	return pin
}

func internFavoriteList(pool *stringPool, list FavoriteList) FavoriteList {
	list.ID = pool.Intern(list.ID)
	list.Name = pool.Intern(list.Name)
	return list
}

func internFavoriteMod(pool *stringPool, mod FavoriteMod) FavoriteMod {
	mod.ID = pool.Intern(mod.ID)
	mod.ListID = pool.Intern(mod.ListID)
	mod.Platform = pool.Intern(mod.Platform)
	mod.ModID = pool.Intern(mod.ModID)
	mod.VersionID = pool.Intern(mod.VersionID)
	mod.MinecraftVersion = pool.Intern(mod.MinecraftVersion)
	mod.ModLoader = pool.Intern(mod.ModLoader)
	mod.Title = pool.Intern(mod.Title)
	mod.Slug = pool.Intern(mod.Slug)
	mod.IconURL = pool.Intern(mod.IconURL)
	mod.Description = pool.Intern(mod.Description)
	mod.Categories = internStringSlice(pool, mod.Categories)
	return mod
}

func (d *cacheDB) view(fn func(*cacheState) error) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return fn(&d.state)
}

func (d *cacheDB) update(fn func(*cacheState, *stringPool) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fn(&d.state, &d.strings)
}

func (d *cacheDB) save() error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(d.path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.path), filepath.Base(d.path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	zw, err := zstd.NewWriter(tmp)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	encodeErr := gob.NewEncoder(zw).Encode(d.state)
	closeZErr := zw.Close()
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if encodeErr != nil {
		return encodeErr
	}
	if closeZErr != nil {
		return closeZErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpPath, d.path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func copyStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func copyDependencies(deps []models.ModDependency) []models.ModDependency {
	if len(deps) == 0 {
		return nil
	}
	out := make([]models.ModDependency, len(deps))
	copy(out, deps)
	return out
}

func copyProject(p models.ModProject) models.ModProject {
	p.Categories = copyStringSlice(p.Categories)
	return p
}

func copyVersion(v models.ModVersion) models.ModVersion {
	v.GameVersions = copyStringSlice(v.GameVersions)
	v.Loaders = copyStringSlice(v.Loaders)
	v.Dependencies = copyDependencies(v.Dependencies)
	v.ModIDs = copyStringSlice(v.ModIDs)
	return v
}

func copyFavoriteMod(mod FavoriteMod) FavoriteMod {
	mod.Categories = copyStringSlice(mod.Categories)
	return mod
}
