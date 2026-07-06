package appcore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/link-fgfgui/mod-downloader-core/database"
	"github.com/link-fgfgui/mod-downloader-core/global"
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
