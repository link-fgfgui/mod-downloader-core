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
