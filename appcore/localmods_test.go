package appcore

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/modbridge"
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
