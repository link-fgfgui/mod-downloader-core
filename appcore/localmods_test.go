package appcore

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/link-fgfgui/mod-downloader-core/database"
	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/modbridge"
	"github.com/link-fgfgui/mod-downloader-core/models"
	structs "github.com/link-fgfgui/mod-downloader-core/structs/minecraft"
)

func TestLocalModsInDirScansExplicitModsDir(t *testing.T) {
	global.SetMinecraftDir("")
	global.ClearLocalMods()
	t.Cleanup(func() {
		global.SetMinecraftDir("")
		global.ClearLocalMods()
	})

	modsDir := filepath.Join(t.TempDir(), "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFabricJar(t, filepath.Join(modsDir, "example.jar"))

	svc := New(Options{})
	mods, err := svc.LocalModsInDir(modsDir, "1.21.1", "fabric")
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 1 {
		t.Fatalf("mod count = %d, want 1: %#v", len(mods), mods)
	}
	if mods[0].ID != "example" || mods[0].FileName != "example" || mods[0].Path != filepath.Join(modsDir, "example.jar") {
		t.Fatalf("mod = %#v", mods[0])
	}

	paths := global.LocalModPathsInInstance(modbridge.InstanceIDForTargetDir(modsDir))
	if len(paths) != 1 {
		t.Fatalf("indexed paths = %#v, want 1", paths)
	}
	if paths[0].Path != filepath.Join(modsDir, "example.jar") {
		t.Fatalf("indexed path = %q", paths[0].Path)
	}
}

func TestEnrichModMetadataPrefersOnlineDisplayFields(t *testing.T) {
	if err := database.OpenAt(filepath.Join(t.TempDir(), database.CacheFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	project := models.ModProject{
		Platform:   "Modrinth",
		ProjectID:  "project-1",
		Slug:       "online-project",
		Title:      "Online Project",
		IconURL:    "https://example.com/icon.png",
		Categories: []string{"library", "magic"},
	}
	if err := database.UpsertModPlatform(project); err != nil {
		t.Fatal(err)
	}
	if err := database.SetPlatformVersions("Modrinth", "project-1", []models.ModVersion{{
		ID:        "version-1",
		VersionID: "version-1",
		ProjectID: "project-1",
		Platform:  "Modrinth",
		SHA1:      "abc123",
	}}); err != nil {
		t.Fatal(err)
	}

	enriched, misses := enrichModMetadata([]structs.ModInfo{{
		ID:      "jar-id",
		Name:    "Jar Name",
		Version: "1.0.0",
		SHA1:    "abc123",
		JijMods: []structs.JijModInfo{{ID: "nested", Name: "Nested"}},
	}})
	if len(misses) != 0 {
		t.Fatalf("misses = %#v, want none", misses)
	}
	if len(enriched) != 1 {
		t.Fatalf("enriched count = %d, want 1", len(enriched))
	}
	got := enriched[0]
	if got.ID != "jar-id" || got.Name != "Jar Name" || got.Version != "1.0.0" || len(got.JijMods) != 1 {
		t.Fatalf("jar-derived fields changed: %#v", got)
	}
	if got.OnlineName != "Online Project" || got.IconURL != "https://example.com/icon.png" || got.OnlinePlatform != "Modrinth" || got.OnlineProjectID != "project-1" || got.OnlineSlug != "online-project" {
		t.Fatalf("online fields = %#v", got)
	}
	assertStringSlice(t, got.Categories, []string{"library", "magic"})
}

func assertStringSlice(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strings = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("strings[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
	}
}

func writeFabricJar(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create("fabric.mod.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"id":"example","name":"Example","version":"1.0.0"}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
