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

	now := time.Now().Unix()
	if err := database.UpsertModPlatform(models.ModProject{
		ID:        models.ProjectKey("modrinth", "sodium"),
		Platform:  "Modrinth",
		ProjectID: "sodium",
		Title:     "Sodium",
		CachedAt:  now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetPlatformVersionSnapshot("Modrinth", "sodium", []models.ModVersion{{
		ID:           "version-1",
		Platform:     "Modrinth",
		ProjectID:    "sodium",
		VersionID:    "version-1",
		Name:         "Sodium 1",
		FileName:     "sodium.jar",
		DownloadURL:  "https://cdn.modrinth.com/data/sodium.jar",
		SHA1:         "abc123",
		GameVersions: []string{"1.21.1"},
		Loaders:      []string{"fabric"},
	}}, now, []database.ModPlatformVersionScope{{
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
	}}); err != nil {
		t.Fatal(err)
	}

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
	zr, err := zip.OpenReader(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	paths := make(map[string]bool)
	for _, file := range zr.File {
		paths[file.Name] = true
	}
	for _, path := range []string{"pack.toml", "index.toml", "mods/sodium.pw.toml"} {
		if !paths[path] {
			t.Fatalf("missing %s from exported zip paths %#v", path, paths)
		}
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
