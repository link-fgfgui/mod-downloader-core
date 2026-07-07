package appcore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/link-fgfgui/mod-downloader-core/configs"
	"github.com/link-fgfgui/mod-downloader-core/database"
	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/minecraft"
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
