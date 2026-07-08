package appcore

import (
	"archive/zip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/link-fgfgui/mod-downloader-core/database"
	"github.com/link-fgfgui/mod-downloader-core/models"
)

func TestFavoritePackwizScopeRejectsMixedOrMissingScope(t *testing.T) {
	_, err := favoritePackwizScope([]database.FavoriteMod{{
		Title:            "Sodium",
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
	}, {
		Title:            "JEI",
		MinecraftVersion: "1.20.1",
		ModLoader:        "fabric",
	}})
	if err == nil || !strings.Contains(err.Error(), "multiple minecraft versions") {
		t.Fatalf("mixed minecraft error = %v", err)
	}

	_, err = favoritePackwizScope([]database.FavoriteMod{{
		Title:            "Sodium",
		MinecraftVersion: "1.21.1",
	}})
	if err == nil || !strings.Contains(err.Error(), "missing minecraft version or mod loader") {
		t.Fatalf("missing scope error = %v", err)
	}
}

func TestExportFavoriteListPackwizZipRejectsEmptyList(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	svc := New(Options{})
	list := svc.CreateFavoriteList("Empty")
	_, err := svc.ExportFavoriteListPackwizZip(list.ID, filepath.Join(t.TempDir(), "empty.zip"))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty list error = %v", err)
	}
}

func TestExportFavoriteListPackwizZipWritesPack(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	storeFavoritePackwizVersion(t, "modrinth", "sodium", "version-1", "1.21.1", "fabric", "sodium.jar")

	svc := New(Options{})
	list := svc.CreateFavoriteList("Performance")
	saved := svc.AddFavoriteMod(database.FavoriteMod{
		ListID:           list.ID,
		Platform:         "modrinth",
		ModID:            "sodium",
		VersionID:        "version-1",
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
		Title:            "Sodium",
	})
	if saved.ID == "" {
		t.Fatalf("favorite mod not saved: %#v", saved)
	}

	out := filepath.Join(t.TempDir(), "performance")
	result, err := svc.ExportFavoriteListPackwizZip(list.ID, out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.Path, ".zip") {
		t.Fatalf("result path = %q", result.Path)
	}
	paths := zipPaths(t, result.Path)
	for _, path := range []string{"pack.toml", "index.toml", "mods/sodium.pw.toml"} {
		if !paths[path] {
			t.Fatalf("missing %s from exported zip paths %#v", path, paths)
		}
	}
}

func TestExportFavoriteListPackwizZipForScopeFiltersItems(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	storeFavoritePackwizVersion(t, "modrinth", "sodium", "version-1", "1.21.1", "fabric", "sodium.jar")

	svc := New(Options{})
	list := svc.CreateFavoriteList("Mixed")
	svc.AddFavoriteMod(database.FavoriteMod{
		ListID:           list.ID,
		Platform:         "modrinth",
		ModID:            "sodium",
		VersionID:        "version-1",
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
		Title:            "Sodium",
	})
	svc.AddFavoriteMod(database.FavoriteMod{
		ListID:           list.ID,
		Platform:         "modrinth",
		ModID:            "lithium",
		VersionID:        "hidden-version",
		MinecraftVersion: "1.20.1",
		ModLoader:        "forge",
		Title:            "Lithium",
	})

	result, err := svc.ExportFavoriteListPackwizZipForScope(list.ID, filepath.Join(t.TempDir(), "scoped"), "1.21.1", "Fabric")
	if err != nil {
		t.Fatal(err)
	}
	paths := zipPaths(t, result.Path)
	if !paths["mods/sodium.pw.toml"] {
		t.Fatalf("missing scoped mod from exported zip paths %#v", paths)
	}
	if paths["mods/lithium.pw.toml"] {
		t.Fatalf("hidden scope mod exported in zip paths %#v", paths)
	}
}

func TestEnsureZipPathAppendsExtension(t *testing.T) {
	if got := ensureZipPath("/tmp/pack"); got != "/tmp/pack.zip" {
		t.Fatalf("ensureZipPath without extension = %q", got)
	}
	if got := ensureZipPath("/tmp/pack.ZIP"); got != "/tmp/pack.ZIP" {
		t.Fatalf("ensureZipPath with extension = %q", got)
	}
}

func storeFavoritePackwizVersion(t *testing.T, platform, projectID, versionID, minecraftVersion, modLoader, fileName string) {
	t.Helper()
	now := time.Now().Unix()
	if err := database.UpsertModPlatform(models.ModProject{
		ID:        models.ProjectKey(platform, projectID),
		Platform:  platform,
		ProjectID: projectID,
		Title:     projectID,
		CachedAt:  now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetPlatformVersionSnapshot(platform, projectID, []models.ModVersion{{
		ID:           versionID,
		Platform:     platform,
		ProjectID:    projectID,
		VersionID:    versionID,
		Name:         versionID,
		FileName:     fileName,
		DownloadURL:  "https://cdn.modrinth.com/data/" + fileName,
		SHA1:         "abc123",
		GameVersions: []string{minecraftVersion},
		Loaders:      []string{modLoader},
	}}, now, []database.ModPlatformVersionScope{{
		MinecraftVersion: minecraftVersion,
		ModLoader:        modLoader,
	}}); err != nil {
		t.Fatal(err)
	}
}

func zipPaths(t *testing.T, path string) map[string]bool {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	paths := make(map[string]bool)
	for _, file := range zr.File {
		paths[file.Name] = true
	}
	return paths
}
