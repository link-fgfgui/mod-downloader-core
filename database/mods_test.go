package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/link-fgfgui/mod-downloader-core/models"
)

func openTestDB(t *testing.T) string {
	t.Helper()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		Close()
		if err := os.Chdir(oldwd); err != nil {
			t.Fatal(err)
		}
	})
	path := filepath.Join(tmp, databaseFileName)
	if err := OpenAt(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func reopenTestDB(t *testing.T, path string) {
	t.Helper()
	Close()
	if err := OpenAt(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultCachePathUsesTempDir(t *testing.T) {
	path, err := DefaultCachePath()
	if err != nil {
		t.Fatal(err)
	}
	tempDir := filepath.Clean(os.TempDir())
	if rel, err := filepath.Rel(tempDir, path); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		t.Fatalf("DefaultCachePath() = %q, want under %q", path, tempDir)
	}
	if filepath.Base(path) != databaseFileName {
		t.Fatalf("DefaultCachePath() base = %q, want %q", filepath.Base(path), databaseFileName)
	}
}

func TestOpenAtCreatesParentDirectoryOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "cache", databaseFileName)
	if err := OpenAt(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("parent after open: %v", err)
	}
	if _, err := os.Stat(UserDataPathForCachePath(path)); err != nil {
		t.Fatalf("user database after open: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cache file before close error = %v, want not exist", err)
	}
	Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file after close: %v", err)
	}
}

func TestCachePlatformVersionsAndDependencies(t *testing.T) {
	path := openTestDB(t)

	if err := UpsertModPlatform(models.ModProject{
		Platform:  "Modrinth",
		ProjectID: "sodium",
		Slug:      "sodium-slug",
		Title:     "Sodium",
	}); err != nil {
		t.Fatal(err)
	}
	if err := SetPlatformVersionSnapshot("Modrinth", "sodium", []models.ModVersion{
		{
			VersionID:    "v1",
			Name:         "1.0",
			SHA1:         "abc",
			PublishedAt:  10,
			GameVersions: []string{"1.21.1"},
			Loaders:      []string{"neoforge"},
			Dependencies: []models.ModDependency{{
				DependencyProjectID: "fabric-api",
				DependencyType:      "required",
			}},
		},
	}, 100, []ModPlatformVersionScope{{
		MinecraftVersion: "1.21.1",
		ModLoader:        "NeoForge",
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cache file before close error = %v, want not exist", err)
	}

	reopenTestDB(t, path)

	if p, ok := GetModPlatformBySlug("Modrinth", "sodium-slug"); !ok || p.ProjectID != "sodium" {
		t.Fatalf("slug lookup = %#v, %v", p, ok)
	}
	if ts, ok := GetPlatformVersionScopeUpdatedAt("Modrinth", "sodium", ModPlatformVersionScope{MinecraftVersion: "1.21.1", ModLoader: "neoforge"}); !ok || ts != 100 {
		t.Fatalf("scope timestamp = %d, %v", ts, ok)
	}

	versions, err := GetPlatformVersions("Modrinth", "sodium")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].VersionID != "v1" || len(versions[0].Dependencies) != 1 {
		t.Fatalf("versions = %#v", versions)
	}
	if versions[0].Dependencies[0].PlatformVersionID != versions[0].ID {
		t.Fatalf("dependency platform version id = %q, want %q", versions[0].Dependencies[0].PlatformVersionID, versions[0].ID)
	}

	deps, err := GetVersionDependencies(versions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0].DependencyProjectID != "fabric-api" {
		t.Fatalf("dependencies = %#v", deps)
	}
}

func TestPlatformCacheKeysAreCaseInsensitive(t *testing.T) {
	openTestDB(t)

	if err := UpsertModPlatform(models.ModProject{
		Platform:  "Modrinth",
		ProjectID: "sodium",
		Slug:      "sodium-slug",
		Title:     "Sodium",
	}); err != nil {
		t.Fatal(err)
	}
	if err := SetPlatformVersionSnapshot("Modrinth", "sodium", []models.ModVersion{
		{
			VersionID:   "v1",
			SHA1:        "abc",
			PublishedAt: 10,
		},
	}, 100, []ModPlatformVersionScope{{
		MinecraftVersion: "1.21.1",
		ModLoader:        "NeoForge",
	}}); err != nil {
		t.Fatal(err)
	}

	if p, ok := GetModPlatform("modrinth", "sodium"); !ok || p.Title != "Sodium" || p.Platform != "Modrinth" {
		t.Fatalf("GetModPlatform lower-case = %#v, %v", p, ok)
	}
	if p, ok := GetModPlatformBySlug("modrinth", "sodium-slug"); !ok || p.ProjectID != "sodium" {
		t.Fatalf("GetModPlatformBySlug lower-case = %#v, %v", p, ok)
	}
	if versions, err := GetPlatformVersions("modrinth", "sodium"); err != nil || len(versions) != 1 || versions[0].VersionID != "v1" {
		t.Fatalf("GetPlatformVersions lower-case = %#v, %v", versions, err)
	}
	if ts, ok := GetPlatformVersionScopeUpdatedAt("modrinth", "sodium", ModPlatformVersionScope{MinecraftVersion: "1.21.1", ModLoader: "neoforge"}); !ok || ts != 100 {
		t.Fatalf("GetPlatformVersionScopeUpdatedAt lower-case = %d, %v", ts, ok)
	}
	if version, ok := GetLatestProjectBySHA1("modrinth", "abc"); !ok || version.VersionID != "v1" || version.Platform != "Modrinth" {
		t.Fatalf("GetLatestProjectBySHA1 lower-case = %#v, %v", version, ok)
	}
}

func TestCachePinnedMods(t *testing.T) {
	path := openTestDB(t)

	if err := UpsertPinnedMod(PinnedMod{
		Platform:         "Modrinth",
		ModID:            "Sodium",
		VersionID:        "v1",
		MinecraftVersion: "1.21.1",
		ModLoader:        "NeoForge",
	}); err != nil {
		t.Fatal(err)
	}

	reopenTestDB(t, path)

	if pin, ok := GetPinnedMod("modrinth", "sodium", "1.21.1", "neoforge"); !ok || pin.VersionID != "v1" {
		t.Fatalf("pin = %#v, %v", pin, ok)
	}
}

func TestListPinnedMods(t *testing.T) {
	path := openTestDB(t)

	if pins := ListPinnedMods(); len(pins) != 0 {
		t.Fatalf("empty list = %#v, want empty", pins)
	}

	inserts := []PinnedMod{
		{Platform: "CurseForge", ModID: "jei", VersionID: "v3", MinecraftVersion: "1.20.1", ModLoader: "forge"},
		{Platform: "Modrinth", ModID: "sodium", VersionID: "v1", MinecraftVersion: "1.21.1", ModLoader: "fabric"},
		{Platform: "Modrinth", ModID: "sodium", VersionID: "v2", MinecraftVersion: "1.21.1", ModLoader: "neoforge"},
	}
	for _, p := range inserts {
		if err := UpsertPinnedMod(p); err != nil {
			t.Fatal(err)
		}
	}

	pins := ListPinnedMods()
	if len(pins) != 3 {
		t.Fatalf("len = %d, want 3", len(pins))
	}

	want := []PinnedMod{
		{Platform: "curseforge", ModID: "jei", VersionID: "v3", MinecraftVersion: "1.20.1", ModLoader: "forge"},
		{Platform: "modrinth", ModID: "sodium", VersionID: "v1", MinecraftVersion: "1.21.1", ModLoader: "fabric"},
		{Platform: "modrinth", ModID: "sodium", VersionID: "v2", MinecraftVersion: "1.21.1", ModLoader: "neoforge"},
	}
	for i, p := range pins {
		if p.Platform != want[i].Platform || p.ModID != want[i].ModID || p.VersionID != want[i].VersionID || p.MinecraftVersion != want[i].MinecraftVersion || p.ModLoader != want[i].ModLoader {
			t.Fatalf("pin[%d] = %#v, want %#v", i, p, want[i])
		}
	}

	pins[0].VersionID = "mutated"
	if got, ok := GetPinnedMod("curseforge", "jei", "1.20.1", "forge"); !ok || got.VersionID != "v3" {
		t.Fatalf("returned value was not a copy: %#v", got)
	}

	reopenTestDB(t, path)

	if len(ListPinnedMods()) != 3 {
		t.Fatalf("after reopen len = %d, want 3", len(ListPinnedMods()))
	}
}

func TestFavoriteListsAndModsPersist(t *testing.T) {
	path := openTestDB(t)

	first, err := CreateFavoriteList(" Utilities ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateFavoriteList("Performance")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || second.ID == "" || first.ID == second.ID {
		t.Fatalf("list IDs = %q, %q", first.ID, second.ID)
	}
	if first.Name != "Utilities" || second.SortOrder <= first.SortOrder {
		t.Fatalf("lists = %#v, %#v", first, second)
	}

	renamed, ok, err := UpdateFavoriteListName(first.ID, "Utility Mods")
	if err != nil || !ok || renamed.Name != "Utility Mods" {
		t.Fatalf("rename = %#v, %v, %v", renamed, ok, err)
	}

	item, ok, err := UpsertFavoriteMod(FavoriteMod{
		ListID:           first.ID,
		Platform:         "Modrinth",
		ModID:            "Sodium",
		VersionID:        "v1",
		MinecraftVersion: "1.21.1",
		ModLoader:        "Fabric",
		Title:            "Sodium",
		Slug:             "sodium",
		IconURL:          "https://example.com/icon.png",
		Categories:       []string{"Performance", "performance", ""},
	})
	if err != nil || !ok {
		t.Fatalf("upsert favorite mod = %#v, %v, %v", item, ok, err)
	}
	if item.Platform != "modrinth" || item.ModID != "sodium" || item.ModLoader != "fabric" {
		t.Fatalf("normalized item = %#v", item)
	}
	if len(item.Categories) != 1 || item.Categories[0] != "performance" {
		t.Fatalf("categories = %#v", item.Categories)
	}

	updated, ok, err := UpsertFavoriteMod(FavoriteMod{
		ListID:           first.ID,
		Platform:         "modrinth",
		ModID:            "sodium",
		VersionID:        "v2",
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
		Title:            "Sodium Updated",
	})
	if err != nil || !ok {
		t.Fatalf("update favorite mod = %#v, %v, %v", updated, ok, err)
	}
	if updated.ID != item.ID || updated.VersionID != "v2" || updated.Title != "Sodium Updated" {
		t.Fatalf("updated item = %#v, original = %#v", updated, item)
	}

	reopenTestDB(t, path)

	lists := ListFavoriteLists()
	if len(lists) != 2 {
		t.Fatalf("lists after reopen = %#v", lists)
	}
	if lists[0].ID != first.ID || lists[0].Name != "Utility Mods" || lists[1].ID != second.ID {
		t.Fatalf("list order after reopen = %#v", lists)
	}

	mods := ListFavoriteMods(first.ID)
	if len(mods) != 1 || mods[0].ID != item.ID || mods[0].VersionID != "v2" || mods[0].Title != "Sodium Updated" {
		t.Fatalf("mods after reopen = %#v", mods)
	}

	mods[0].Categories = []string{"mutated"}
	if again := ListFavoriteMods(first.ID); len(again) != 1 || len(again[0].Categories) != 0 {
		t.Fatalf("returned favorite mods are not copies: %#v", again)
	}
}

func TestFavoriteListDeleteCascadesItems(t *testing.T) {
	openTestDB(t)

	list, err := CreateFavoriteList("To Delete")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := UpsertFavoriteMod(FavoriteMod{
		ListID:   list.ID,
		Platform: "modrinth",
		ModID:    "sodium",
	}); err != nil || !ok {
		t.Fatalf("upsert favorite mod: %v, %v", ok, err)
	}
	if deleted, err := DeleteFavoriteList(list.ID); err != nil || !deleted {
		t.Fatalf("delete favorite list = %v, %v", deleted, err)
	}
	if lists := ListFavoriteLists(); len(lists) != 0 {
		t.Fatalf("lists after delete = %#v", lists)
	}
	if mods := ListFavoriteMods(list.ID); len(mods) != 0 {
		t.Fatalf("mods after delete = %#v", mods)
	}
}

func TestFavoriteModRequiresExistingList(t *testing.T) {
	openTestDB(t)

	mod, ok, err := UpsertFavoriteMod(FavoriteMod{
		ListID:   "missing",
		Platform: "modrinth",
		ModID:    "sodium",
	})
	if err != nil || ok || mod.ID != "" {
		t.Fatalf("upsert without list = %#v, %v, %v", mod, ok, err)
	}
}

func TestLegacyGobUserDataMigratesToSQLite(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, databaseFileName)
	state := newCacheState()
	state.PinnedMods[makePinnedModKey("Modrinth", "Sodium", "1.21.1", "Fabric")] = PinnedMod{
		Platform:         "Modrinth",
		ModID:            "Sodium",
		VersionID:        "v1",
		MinecraftVersion: "1.21.1",
		ModLoader:        "Fabric",
	}
	listID := "legacy-list"
	state.FavoriteLists[listID] = FavoriteList{
		ID:        listID,
		Name:      "Legacy",
		CreatedAt: 10,
		UpdatedAt: 11,
		SortOrder: 1,
	}
	state.FavoriteMods[makeFavoriteModKey(listID, "Modrinth", "Sodium", "1.21.1", "Fabric")] = FavoriteMod{
		ListID:           listID,
		Platform:         "Modrinth",
		ModID:            "Sodium",
		VersionID:        "v1",
		MinecraftVersion: "1.21.1",
		ModLoader:        "Fabric",
		Title:            "Sodium",
		Categories:       []string{"performance"},
		CreatedAt:        12,
		UpdatedAt:        13,
	}
	legacyDB := &cacheDB{path: path, state: state, strings: newStringPool()}
	if err := legacyDB.save(); err != nil {
		t.Fatal(err)
	}

	if err := OpenAt(path); err != nil {
		t.Fatal(err)
	}
	if pin, ok := GetPinnedMod("modrinth", "sodium", "1.21.1", "fabric"); !ok || pin.VersionID != "v1" {
		t.Fatalf("migrated pin = %#v, %v", pin, ok)
	}
	lists := ListFavoriteLists()
	if len(lists) != 1 || lists[0].ID != listID || lists[0].Name != "Legacy" {
		t.Fatalf("migrated lists = %#v", lists)
	}
	mods := ListFavoriteMods(listID)
	if len(mods) != 1 || mods[0].VersionID != "v1" || len(mods[0].Categories) != 1 {
		t.Fatalf("migrated mods = %#v", mods)
	}
	Close()

	reloaded, err := loadCacheState(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.normalize()
	if len(reloaded.PinnedMods) != 0 || len(reloaded.FavoriteLists) != 0 || len(reloaded.FavoriteMods) != 0 {
		t.Fatalf("legacy user data still in gob cache: pins=%d lists=%d mods=%d", len(reloaded.PinnedMods), len(reloaded.FavoriteLists), len(reloaded.FavoriteMods))
	}

	if err := OpenAt(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	if pin, ok := GetPinnedMod("modrinth", "sodium", "1.21.1", "fabric"); !ok || pin.VersionID != "v1" {
		t.Fatalf("sqlite pin after reopen = %#v, %v", pin, ok)
	}
}

func TestCacheVersionModIDs(t *testing.T) {
	path := openTestDB(t)

	if err := UpsertModPlatform(models.ModProject{
		Platform:  "Modrinth",
		ProjectID: "sodium",
		Slug:      "sodium-slug",
		Title:     "Sodium",
	}); err != nil {
		t.Fatal(err)
	}
	if err := SetPlatformVersionSnapshot("Modrinth", "sodium", []models.ModVersion{
		{
			VersionID:    "v1",
			Name:         "1.0",
			SHA1:         "abc",
			PublishedAt:  10,
			GameVersions: []string{"1.21.1"},
			Loaders:      []string{"neoforge"},
		},
	}, 100, []ModPlatformVersionScope{{
		MinecraftVersion: "1.21.1",
		ModLoader:        "NeoForge",
	}}); err != nil {
		t.Fatal(err)
	}

	versions, err := GetPlatformVersions("Modrinth", "sodium")
	if err != nil || len(versions) != 1 {
		t.Fatalf("get versions: %v, count=%d", err, len(versions))
	}

	if err := SetVersionModIDs(versions[0].ID, []string{"sodium", "Sodium", "other", ""}); err != nil {
		t.Fatal(err)
	}

	// Direct read-back by version ID must reflect the persisted, deduplicated IDs.
	direct, err := GetVersionModIDs(versions[0].ID)
	if err != nil {
		t.Fatalf("GetVersionModIDs error: %v", err)
	}
	if len(direct) != 2 || direct[0] != "sodium" || direct[1] != "other" {
		t.Fatalf("GetVersionModIDs = %#v", direct)
	}

	// Unknown version ID must return (nil, nil) — callers treat this as a cache miss.
	miss, err := GetVersionModIDs("does-not-exist")
	if err != nil || miss != nil {
		t.Fatalf("GetVersionModIDs unknown = %#v, %v", miss, err)
	}

	reopenTestDB(t, path)

	versions, err = GetPlatformVersions("Modrinth", "sodium")
	if err != nil || len(versions) != 1 {
		t.Fatalf("get versions after reopen: %v, count=%d", err, len(versions))
	}
	modIDs := versions[0].ModIDs
	if len(modIDs) != 2 || modIDs[0] != "sodium" || modIDs[1] != "other" {
		t.Fatalf("modIDs = %#v", modIDs)
	}

	// Direct read-back must survive reopen.
	direct, err = GetVersionModIDs(versions[0].ID)
	if err != nil {
		t.Fatalf("GetVersionModIDs after reopen error: %v", err)
	}
	if len(direct) != 2 || direct[0] != "sodium" || direct[1] != "other" {
		t.Fatalf("GetVersionModIDs after reopen = %#v", direct)
	}
}
