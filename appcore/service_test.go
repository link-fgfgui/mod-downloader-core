package appcore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/link-fgfgui/mod-downloader-core/configs"
	"github.com/link-fgfgui/mod-downloader-core/database"
	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/minecraft"
	"github.com/link-fgfgui/mod-downloader-core/models"
)

func TestCoreDoesNotDependOnWailsRuntime(t *testing.T) {
	packages := []string{
		"github.com/link-fgfgui/mod-downloader-core/appcore",
		"github.com/link-fgfgui/mod-downloader-core/httpserver",
	}
	args := append([]string{"list", "-deps"}, packages...)
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		t.Fatalf("go list deps failed: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if pkg == "github.com/wailsapp/wails/v2/pkg/runtime" {
			t.Fatalf("core dependency tree includes Wails runtime")
		}
	}
}

func TestStartupNoConfigFileUsesEnvironmentOnly(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		global.SetMinecraftDir("")
		if err := os.Chdir(oldwd); err != nil {
			t.Fatal(err)
		}
	})

	if err := os.WriteFile(filepath.Join(dir, "mod-downloader.toml"), []byte(`
[keys]
curseforge_api_key = "from-file"
[preferences]
minecraft_dir = "/from-file"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEYS_CF_API_KEY", "from-env")

	cachePath := filepath.Join(t.TempDir(), "cache", database.CacheFileName)
	svc := New(Options{Runtime: RuntimeOptions{
		NoConfigFile: true,
		CachePath:    cachePath,
	}})
	if err := svc.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if svc.Config().Keys.CurseforgeApiKey != "from-env" {
		t.Fatalf("curseforge key = %q, want from-env", svc.Config().Keys.CurseforgeApiKey)
	}
	if got := global.GetMinecraftDir(); got != "" {
		t.Fatalf("global minecraft dir = %q, want empty", got)
	}
	svc.Close()
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache path after close: %v", err)
	}
}

func TestFavoriteServiceWorkflow(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	svc := New(Options{})
	list := svc.CreateFavoriteList("Performance")
	if list.ID == "" || list.Name != "Performance" {
		t.Fatalf("created list = %#v", list)
	}

	renamed := svc.RenameFavoriteList(list.ID, "Performance Mods")
	if renamed.ID != list.ID || renamed.Name != "Performance Mods" {
		t.Fatalf("renamed list = %#v", renamed)
	}

	mod := svc.AddFavoriteMod(database.FavoriteMod{
		ListID:           list.ID,
		Platform:         "Modrinth",
		ModID:            "Sodium",
		MinecraftVersion: "1.21.1",
		ModLoader:        "Fabric",
		Title:            "Sodium",
	})
	if mod.ID == "" || mod.Platform != "modrinth" || mod.ModID != "sodium" {
		t.Fatalf("added mod = %#v", mod)
	}
	if mods := svc.ListFavoriteMods(list.ID); len(mods) != 1 || mods[0].ID != mod.ID {
		t.Fatalf("listed mods = %#v", mods)
	}

	if !svc.RemoveFavoriteMod(list.ID, "Modrinth", "Sodium", "1.21.1", "Fabric") {
		t.Fatal("RemoveFavoriteMod returned false")
	}
	if mods := svc.ListFavoriteMods(list.ID); len(mods) != 0 {
		t.Fatalf("mods after remove = %#v", mods)
	}
	if !svc.DeleteFavoriteList(list.ID) {
		t.Fatal("DeleteFavoriteList returned false")
	}
	if lists := svc.ListFavoriteLists(); len(lists) != 0 {
		t.Fatalf("lists after delete = %#v", lists)
	}
}

func TestFavoriteBulkAddCopiesSelectedMods(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	svc := New(Options{})
	source := svc.CreateFavoriteList("Source")
	target := svc.CreateFavoriteList("Target")
	mod := svc.AddFavoriteMod(database.FavoriteMod{
		ListID:           source.ID,
		Platform:         "Modrinth",
		ModID:            "Sodium",
		MinecraftVersion: "1.21.1",
		ModLoader:        "Fabric",
		Title:            "Sodium",
	})
	if mod.ID == "" {
		t.Fatalf("added source mod = %#v", mod)
	}

	result := svc.AddFavoriteModsToLists(FavoriteBulkAddRequest{
		TargetListIDs: []string{target.ID, "missing", target.ID},
		Mods:          []database.FavoriteMod{mod},
	})
	if result.Added != 1 || result.Updated != 0 || result.Skipped != 1 || len(result.Errors) != 1 {
		t.Fatalf("bulk add result = %#v", result)
	}
	targetMods := svc.ListFavoriteMods(target.ID)
	if len(targetMods) != 1 || targetMods[0].ID == mod.ID || targetMods[0].Title != "Sodium" {
		t.Fatalf("target mods = %#v, source = %#v", targetMods, mod)
	}

	mod.Title = "Sodium Updated"
	result = svc.AddFavoriteModsToLists(FavoriteBulkAddRequest{
		TargetListIDs: []string{target.ID},
		Mods:          []database.FavoriteMod{mod},
	})
	if result.Added != 0 || result.Updated != 1 || result.Skipped != 0 {
		t.Fatalf("bulk update result = %#v", result)
	}
	targetMods = svc.ListFavoriteMods(target.ID)
	if len(targetMods) != 1 || targetMods[0].Title != "Sodium Updated" {
		t.Fatalf("target mods after update = %#v", targetMods)
	}
}

func TestFavoriteListCopyUsesResolvedReferences(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	svc := New(Options{})
	source := svc.CreateFavoriteList("Source")
	child := svc.CreateFavoriteList("Child")
	target := svc.CreateFavoriteList("Target")
	svc.AddFavoriteMod(database.FavoriteMod{ListID: source.ID, Platform: "modrinth", ModID: "sodium", Title: "Sodium"})
	svc.AddFavoriteMod(database.FavoriteMod{ListID: child.ID, Platform: "curseforge", ModID: "jei", Title: "JEI"})
	ref := svc.AddFavoriteListReference(source.ID, child.ID)
	if ref.ID == "" {
		t.Fatal("AddFavoriteListReference returned empty ref")
	}

	result := svc.CopyFavoriteListToList(FavoriteListCopyRequest{SourceListID: source.ID, TargetListID: target.ID})
	if result.Added != 2 || result.Updated != 0 || result.Skipped != 0 {
		t.Fatalf("copy result = %#v", result)
	}
	targetMods := svc.ListFavoriteMods(target.ID)
	if len(targetMods) != 2 {
		t.Fatalf("target mods = %#v", targetMods)
	}

	self := svc.CopyFavoriteListToList(FavoriteListCopyRequest{SourceListID: source.ID, TargetListID: source.ID})
	if self.Skipped != 2 || len(self.Errors) != 1 {
		t.Fatalf("self copy result = %#v", self)
	}
}

func TestFavoriteListReferenceServiceMethods(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	svc := New(Options{})
	parent := svc.CreateFavoriteList("Parent")
	child := svc.CreateFavoriteList("Child")

	ref := svc.AddFavoriteListReference(parent.ID, child.ID)
	if ref.ID == "" || ref.ParentListID != parent.ID || ref.ChildListID != child.ID {
		t.Fatalf("ref = %#v", ref)
	}
	refs := svc.ListFavoriteListRefs(parent.ID)
	if len(refs) != 1 || refs[0].ID != ref.ID {
		t.Fatalf("refs = %#v", refs)
	}
	if cycle := svc.AddFavoriteListReference(child.ID, parent.ID); cycle.ID != "" {
		t.Fatalf("cycle ref = %#v", cycle)
	}
	if !svc.RemoveFavoriteListReference(parent.ID, child.ID) {
		t.Fatal("RemoveFavoriteListReference returned false")
	}
	if refs := svc.ListFavoriteListRefs(parent.ID); len(refs) != 0 {
		t.Fatalf("refs after remove = %#v", refs)
	}
}

func TestFavoriteGroupMetadataAndReorderServiceMethods(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	svc := New(Options{})
	first := svc.CreateFavoriteGroup("Utilities")
	second := svc.CreateFavoriteGroup("Performance")
	if first.ID == "" || second.ID == "" {
		t.Fatalf("groups = %#v %#v", first, second)
	}
	if renamed := svc.RenameFavoriteGroup(first.ID, "Utility Mods"); renamed.ID != first.ID || renamed.Name != "Utility Mods" {
		t.Fatalf("renamed group = %#v", renamed)
	}
	if !svc.ReorderFavoriteGroups([]string{second.ID, first.ID}) {
		t.Fatal("ReorderFavoriteGroups returned false")
	}
	groups := svc.ListFavoriteGroups()
	if len(groups) != 2 || groups[0].ID != second.ID || groups[1].ID != first.ID {
		t.Fatalf("groups after reorder = %#v", groups)
	}

	listA := svc.CreateFavoriteList("A")
	listB := svc.CreateFavoriteList("B")
	updated := svc.UpdateFavoriteListMetadata(database.FavoriteList{
		ID:        listA.ID,
		GroupID:   second.ID,
		IconKind:  "mdi",
		IconValue: "mdi-star",
		Pinned:    true,
	})
	if updated.ID != listA.ID || updated.GroupID != second.ID || updated.IconKind != "mdi" || !updated.Pinned {
		t.Fatalf("updated list metadata = %#v", updated)
	}
	if !svc.ReorderFavoriteLists([]string{listB.ID, listA.ID}) {
		t.Fatal("ReorderFavoriteLists returned false")
	}
	lists := svc.ListFavoriteLists()
	if len(lists) != 2 || lists[0].ID != listA.ID || lists[0].SortOrder <= lists[1].SortOrder {
		t.Fatalf("lists after pinned metadata and reorder = %#v", lists)
	}
	if !svc.DeleteFavoriteGroup(second.ID) {
		t.Fatal("DeleteFavoriteGroup returned false")
	}
	lists = svc.ListFavoriteLists()
	for _, list := range lists {
		if list.GroupID == second.ID {
			t.Fatalf("deleted group still assigned to list: %#v", lists)
		}
	}
}

func TestFavoriteMigrationPreviewAndApplyAllMatch(t *testing.T) {
	svc := openFavoriteMigrationService(t)
	storeFavoriteMigrationProject(t, "Modrinth", "sodium", "sodium", []models.ModVersion{
		favoriteMigrationVersion("Modrinth", "sodium", "sodium-1.21.1", "1.21.1", "fabric", 20),
	})

	source := svc.CreateFavoriteList("Source")
	target := svc.CreateFavoriteList("Target")
	sourceMod := svc.AddFavoriteMod(database.FavoriteMod{
		ListID:           source.ID,
		Platform:         "modrinth",
		ModID:            "sodium",
		VersionID:        "sodium-1.20.1",
		MinecraftVersion: "1.20.1",
		ModLoader:        "fabric",
		Title:            "Sodium",
		Slug:             "sodium",
	})

	req := FavoriteMigrationRequest{
		SourceListID:     source.ID,
		TargetListID:     target.ID,
		MinecraftVersion: "1.21.1",
		ModLoader:        "Fabric",
	}
	preview := svc.PreviewFavoriteListMigration(req)
	if len(preview.Matched) != 1 || len(preview.Conflicts) != 0 || len(preview.Errors) != 0 {
		t.Fatalf("preview = %#v", preview)
	}
	if mods := svc.ListFavoriteMods(target.ID); len(mods) != 0 {
		t.Fatalf("preview wrote target mods: %#v", mods)
	}
	if got := preview.Matched[0].Target; got.ListID != target.ID || got.VersionID != "sodium-1.21.1" || got.MinecraftVersion != "1.21.1" || got.ModLoader != "fabric" {
		t.Fatalf("matched target = %#v", got)
	}

	applied := svc.ApplyFavoriteListMigration(req)
	if !applied.Applied || applied.Result.Added != 1 || applied.Result.Updated != 0 || applied.Result.Skipped != 0 {
		t.Fatalf("apply result = %#v", applied)
	}
	targetMods := svc.ListFavoriteMods(target.ID)
	if len(targetMods) != 1 || targetMods[0].ID == sourceMod.ID || targetMods[0].VersionID != "sodium-1.21.1" {
		t.Fatalf("target mods = %#v, source = %#v", targetMods, sourceMod)
	}
	sourceMods := svc.ListFavoriteMods(source.ID)
	if len(sourceMods) != 1 || sourceMods[0].VersionID != "sodium-1.20.1" || sourceMods[0].MinecraftVersion != "1.20.1" {
		t.Fatalf("source mods changed = %#v", sourceMods)
	}
}

func TestFavoriteMigrationPartialConflictRequiresIgnore(t *testing.T) {
	svc := openFavoriteMigrationService(t)
	storeFavoriteMigrationProject(t, "Modrinth", "sodium", "sodium", []models.ModVersion{
		favoriteMigrationVersion("Modrinth", "sodium", "sodium-1.21.1", "1.21.1", "fabric", 20),
	})
	storeFavoriteMigrationProject(t, "Modrinth", "lithium", "lithium", []models.ModVersion{
		favoriteMigrationVersion("Modrinth", "lithium", "lithium-1.20.1", "1.20.1", "fabric", 10),
	})

	source := svc.CreateFavoriteList("Source")
	target := svc.CreateFavoriteList("Target")
	svc.AddFavoriteMod(database.FavoriteMod{ListID: source.ID, Platform: "modrinth", ModID: "sodium", Slug: "sodium", Title: "Sodium"})
	svc.AddFavoriteMod(database.FavoriteMod{ListID: source.ID, Platform: "modrinth", ModID: "lithium", Slug: "lithium", Title: "Lithium"})

	req := FavoriteMigrationRequest{
		SourceListID:     source.ID,
		TargetListID:     target.ID,
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
	}
	preview := svc.PreviewFavoriteListMigration(req)
	if len(preview.Matched) != 1 || len(preview.Conflicts) != 1 {
		t.Fatalf("preview = %#v", preview)
	}

	blocked := svc.ApplyFavoriteListMigration(req)
	if blocked.Applied || blocked.Result.Added != 0 || blocked.Result.Updated != 0 {
		t.Fatalf("blocked apply result = %#v", blocked)
	}
	if mods := svc.ListFavoriteMods(target.ID); len(mods) != 0 {
		t.Fatalf("non-ignore apply wrote target mods: %#v", mods)
	}

	req.IgnoreConflicts = true
	applied := svc.ApplyFavoriteListMigration(req)
	if !applied.Applied || applied.Result.Added != 1 || applied.Result.Skipped != 1 {
		t.Fatalf("ignore-conflict apply result = %#v", applied)
	}
	targetMods := svc.ListFavoriteMods(target.ID)
	if len(targetMods) != 1 || targetMods[0].ModID != "sodium" {
		t.Fatalf("target mods = %#v", targetMods)
	}
}

func TestFavoriteMigrationAllConflictWritesNothing(t *testing.T) {
	svc := openFavoriteMigrationService(t)
	storeFavoriteMigrationProject(t, "Modrinth", "lithium", "lithium", []models.ModVersion{
		favoriteMigrationVersion("Modrinth", "lithium", "lithium-1.20.1", "1.20.1", "fabric", 10),
	})

	source := svc.CreateFavoriteList("Source")
	target := svc.CreateFavoriteList("Target")
	svc.AddFavoriteMod(database.FavoriteMod{ListID: source.ID, Platform: "modrinth", ModID: "lithium", Slug: "lithium", Title: "Lithium"})

	applied := svc.ApplyFavoriteListMigration(FavoriteMigrationRequest{
		SourceListID:     source.ID,
		TargetListID:     target.ID,
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
		IgnoreConflicts:  true,
	})
	if applied.Result.Added != 0 || applied.Result.Updated != 0 || applied.Result.Skipped != 1 {
		t.Fatalf("all-conflict apply result = %#v", applied)
	}
	if mods := svc.ListFavoriteMods(target.ID); len(mods) != 0 {
		t.Fatalf("all-conflict apply wrote target mods: %#v", mods)
	}
}

func TestFavoriteMigrationMissingProjectLookupConflict(t *testing.T) {
	svc := openFavoriteMigrationService(t)
	stubFavoriteMigrationProjectLookup(t)
	source := svc.CreateFavoriteList("Source")
	target := svc.CreateFavoriteList("Target")
	svc.AddFavoriteMod(database.FavoriteMod{ListID: source.ID, Platform: "modrinth", ModID: "missing-project", Title: "Missing"})

	preview := svc.PreviewFavoriteListMigration(FavoriteMigrationRequest{
		SourceListID:     source.ID,
		TargetListID:     target.ID,
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
	})
	if len(preview.Matched) != 0 || len(preview.Conflicts) != 1 || preview.Conflicts[0].Reason != "project not found" {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestFavoriteMigrationUsesReferencedListContents(t *testing.T) {
	svc := openFavoriteMigrationService(t)
	storeFavoriteMigrationProject(t, "CurseForge", "238222", "jei", []models.ModVersion{
		favoriteMigrationVersion("CurseForge", "238222", "jei-1.21.1", "1.21.1", "neoforge", 20),
	})

	source := svc.CreateFavoriteList("Source")
	child := svc.CreateFavoriteList("Child")
	target := svc.CreateFavoriteList("Target")
	svc.AddFavoriteMod(database.FavoriteMod{ListID: child.ID, Platform: "curseforge", ModID: "238222", Slug: "jei", Title: "JEI"})
	if ref := svc.AddFavoriteListReference(source.ID, child.ID); ref.ID == "" {
		t.Fatal("AddFavoriteListReference returned empty ref")
	}

	preview := svc.PreviewFavoriteListMigration(FavoriteMigrationRequest{
		SourceListID:     source.ID,
		TargetListID:     target.ID,
		MinecraftVersion: "1.21.1",
		ModLoader:        "NeoForge",
	})
	if len(preview.Matched) != 1 || len(preview.Conflicts) != 0 {
		t.Fatalf("preview = %#v", preview)
	}
	if !preview.Matched[0].Source.Referenced || preview.Matched[0].Source.SourceListID != child.ID {
		t.Fatalf("matched source did not come from referenced list: %#v", preview.Matched[0].Source)
	}
}

func openFavoriteMigrationService(t *testing.T) *Service {
	t.Helper()
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return New(Options{})
}

func storeFavoriteMigrationProject(t *testing.T, platform, projectID, slug string, versions []models.ModVersion) {
	t.Helper()
	if err := database.UpsertModPlatform(models.ModProject{
		ID:        models.ProjectKey(platform, projectID),
		Platform:  platform,
		ProjectID: projectID,
		Slug:      slug,
		Title:     projectID,
	}); err != nil {
		t.Fatal(err)
	}
	scopes := []database.ModPlatformVersionScope{
		{MinecraftVersion: "1.21.1", ModLoader: "fabric"},
		{MinecraftVersion: "1.21.1", ModLoader: "neoforge"},
	}
	if err := database.SetPlatformVersionSnapshot(platform, projectID, versions, time.Now().Unix(), scopes); err != nil {
		t.Fatal(err)
	}
}

func favoriteMigrationVersion(platform, projectID, versionID, minecraftVersion, modLoader string, publishedAt int64) models.ModVersion {
	return models.ModVersion{
		ID:           versionID,
		Platform:     platform,
		ProjectID:    projectID,
		VersionID:    versionID,
		Name:         versionID,
		PublishedAt:  publishedAt,
		GameVersions: []string{minecraftVersion},
		Loaders:      []string{modLoader},
	}
}

func stubFavoriteMigrationProjectLookup(t *testing.T) {
	t.Helper()
	lookupByID := favoriteMigrationLookupProjectByID
	lookupBySlug := favoriteMigrationLookupProjectBySlug
	favoriteMigrationLookupProjectByID = func(platform, projectID, mcVersion, modLoader string) (models.ModProject, bool) {
		return models.ModProject{}, false
	}
	favoriteMigrationLookupProjectBySlug = func(platform, slug, mcVersion, modLoader string) (models.ModProject, bool) {
		return models.ModProject{}, false
	}
	t.Cleanup(func() {
		favoriteMigrationLookupProjectByID = lookupByID
		favoriteMigrationLookupProjectBySlug = lookupBySlug
	})
}

func TestSaveCacheDirPreferencePersistsAndReopensDatabase(t *testing.T) {
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	appDir := t.TempDir()
	if err := os.Chdir(appDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		if err := os.Chdir(oldwd); err != nil {
			t.Fatal(err)
		}
	})

	initialDir := filepath.Join(t.TempDir(), "initial-cache")
	svc := New(Options{Config: &configs.Config{
		Runtime: configs.RuntimeConfig{CacheDir: initialDir},
	}})
	if err := svc.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}

	nextDir := filepath.Join(t.TempDir(), "custom-cache")
	view := svc.SaveCacheDirPreference(nextDir)
	if got := minecraft.ExpandPathWithEnv(svc.Config().Runtime.CacheDir); got != nextDir {
		t.Fatalf("config cache dir = %q, want %q", got, nextDir)
	}
	wantCachePath := filepath.Join(nextDir, database.CacheFileName)
	if got := minecraft.ExpandPathWithEnv(view.CachePath); got != wantCachePath {
		t.Fatalf("settings cache path = %q, want %q", got, wantCachePath)
	}
	if _, err := os.Stat(database.UserDataPathForCachePath(wantCachePath)); err != nil {
		t.Fatalf("user database at custom cache dir: %v", err)
	}

	loaded, err := configs.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := minecraft.ExpandPathWithEnv(loaded.Runtime.CacheDir); got != nextDir {
		t.Fatalf("persisted cache dir = %q, want %q", got, nextDir)
	}
}
