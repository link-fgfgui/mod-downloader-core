package downloader

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/link-fgfgui/mod-downloader-core/database"
	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/modbridge"
	"github.com/link-fgfgui/mod-downloader-core/models"
	"github.com/link-fgfgui/mod-downloader-core/providers"
	appstructs "github.com/link-fgfgui/mod-downloader-core/structs"
	mcstructs "github.com/link-fgfgui/mod-downloader-core/structs/minecraft"
)

func resetDownloadQueueForTest(t *testing.T) {
	t.Helper()
	downloadQueue.Lock()
	downloadQueue.nextID = 0
	downloadQueue.pending = nil
	downloadQueue.retryable = nil
	downloadQueue.cancelRequested = nil
	downloadQueue.restartRequested = nil
	downloadQueue.running = false
	downloadQueue.current = nil
	downloadQueue.Unlock()
	resetDownloaderHooksForTest(t)
}

func resetDownloaderHooksForTest(t *testing.T) {
	t.Helper()
	refreshMatchingProjectVersions = providers.RefreshMatchingProjectVersions
	installStatusPrecise = modbridge.InstallStatusPrecise
}

func openDownloadTestDatabase(t *testing.T) {
	t.Helper()
	if err := database.OpenAt(filepath.Join(t.TempDir(), "mods.gob.zst")); err != nil {
		t.Fatalf("database.OpenAt() error = %v", err)
	}
	t.Cleanup(database.Close)
}

func seedDownloadProjectVersion(t *testing.T, projectID string, version models.ModVersion) models.ModProject {
	t.Helper()
	project := models.ModProject{
		ID:        "modrinth:" + projectID,
		Platform:  "Modrinth",
		ProjectID: projectID,
		Slug:      projectID,
		Title:     projectID,
	}
	if err := database.UpsertModPlatform(project); err != nil {
		t.Fatalf("database.UpsertModPlatform(%q) error = %v", projectID, err)
	}
	if err := database.SetPlatformVersionSnapshot("Modrinth", projectID, []models.ModVersion{version}, time.Now().Unix(), []database.ModPlatformVersionScope{{
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
	}}); err != nil {
		t.Fatalf("database.SetPlatformVersionSnapshot(%q) error = %v", projectID, err)
	}
	return project
}

func waitForDownloadState(t *testing.T, match func(appstructs.DownloadQueueState) bool) appstructs.DownloadQueueState {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := GetDownloadQueueState()
		if match(state) {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	state := GetDownloadQueueState()
	t.Fatalf("download state did not match before timeout: %#v", state)
	return state
}

func TestGetDownloadStatesReturnsDefaultWhenSelectedInstanceHasNoLocalMods(t *testing.T) {
	resetDownloadQueueForTest(t)
	global.ClearLocalMods()
	global.SetMinecraftDir(t.TempDir())
	global.SetVersions([]mcstructs.VersionInfo{{
		ID:               "instance",
		Name:             "instance",
		MinecraftVersion: "1.21.1",
		ModLoader:        "neoforge",
	}})
	t.Cleanup(func() {
		global.ClearLocalMods()
		global.SetMinecraftDir("")
		global.InvalidateVersions()
	})

	states := GetDownloadStates(appstructs.DownloadStatesRequest{
		MinecraftVersion: "1.21.1",
		ModLoader:        "NeoForge",
		Results: []models.ModProject{{
			ID:       "modrinth:sodium",
			Platform: "Modrinth",
			Slug:     "sodium",
		}},
	}, nil)

	if len(states) != 1 {
		t.Fatalf("state count = %d, want 1", len(states))
	}
	if states[0].Key != "modrinth:sodium" || states[0].Status != modbridge.BtnStatusNew || states[0].Icon != "mdi-download" || states[0].Color != "primary" || states[0].Disabled {
		t.Fatalf("state = %#v", states[0])
	}
}

func TestPathInLocalModPathsMatchesRelativePaths(t *testing.T) {
	resetDownloadQueueForTest(t)
	mcDir := t.TempDir()
	global.SetMinecraftDir(mcDir)
	t.Cleanup(func() {
		global.SetMinecraftDir("")
	})

	path := filepath.Join(mcDir, "versions", "instance", "mods", "mod.jar")
	paths := []global.LocalModFilePath{{
		Path: filepath.Join("versions", "instance", "mods", "mod.jar"),
	}}
	if !pathInLocalModPaths(path, paths) {
		t.Fatalf("pathInLocalModPaths(%q) = false, want true", path)
	}
}

func TestDependencyDownloadRequestCarriesDependencyVersionID(t *testing.T) {
	resetDownloadQueueForTest(t)
	req, ok := dependencyDownloadRequest("Modrinth", models.ModDependency{
		DependencyProjectID: "fabric-api",
		DependencyVersionID: "version-123",
		DependencyType:      "required",
	}, appstructs.ModDownloadRequest{
		MinecraftVersion: "1.21.1",
		ModLoader:        "Fabric",
		TargetDir:        "/tmp/mods",
		InstanceID:       "cli:/tmp/mods",
	})
	if !ok {
		t.Fatal("dependencyDownloadRequest() ok = false")
	}
	if req.VersionID != "version-123" {
		t.Fatalf("VersionID = %q, want version-123", req.VersionID)
	}
	if req.TargetDir != "/tmp/mods" || req.InstanceID != "cli:/tmp/mods" {
		t.Fatalf("target = %q, instance = %q", req.TargetDir, req.InstanceID)
	}
}

func TestQueueModDownloadAcceptsExplicitTargetWithoutSelectedInstance(t *testing.T) {
	resetDownloadQueueForTest(t)
	global.SetMinecraftDir("")
	global.InvalidateVersions()
	t.Cleanup(func() {
		global.SetMinecraftDir("")
		global.InvalidateVersions()
	})

	result := QueueModDownload(nil, appstructs.ModDownloadRequest{
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
		TargetDir:        filepath.Join(t.TempDir(), "mods"),
	}, "")

	if !result.Skipped {
		t.Fatalf("result = %#v, want skipped invalid request", result)
	}
	if result.Reason != "invalid request" {
		t.Fatalf("reason = %q, want invalid request", result.Reason)
	}
}

func TestCancelDownloadDuringDependencyAnalysisKeepsRetryableQueueItem(t *testing.T) {
	resetDownloadQueueForTest(t)
	openDownloadTestDatabase(t)
	targetDir := t.TempDir()
	mainVersion := models.ModVersion{
		ID:           "main-version",
		Platform:     "Modrinth",
		ProjectID:    "main",
		FileName:     "main.jar",
		DownloadURL:  "https://example.invalid/main.jar",
		GameVersions: []string{"1.21.1"},
		Loaders:      []string{"fabric"},
	}
	project := seedDownloadProjectVersion(t, "main", mainVersion)

	analysisStarted := make(chan struct{})
	releaseAnalysis := make(chan struct{})
	refreshMatchingProjectVersions = func(result models.ModProject, minecraftVersion string, modLoader string) []models.ModVersion {
		close(analysisStarted)
		<-releaseAnalysis
		mainVersion.Dependencies = []models.ModDependency{{
			DependencyProjectID: "dependency",
			DependencyType:      "required",
		}}
		return []models.ModVersion{mainVersion}
	}
	installStatusPrecise = func(req appstructs.ModDownloadRequest) string {
		return modbridge.BtnStatusNew
	}

	result := QueueModDownload(context.Background(), appstructs.ModDownloadRequest{
		ProjectID:        "modrinth:main",
		Result:           project,
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
		TargetDir:        targetDir,
		InstanceID:       "cli:" + targetDir,
	}, "")
	if !result.Queued {
		t.Fatalf("QueueModDownload() = %#v, want queued", result)
	}

	select {
	case <-analysisStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("dependency analysis did not start")
	}
	running := waitForDownloadState(t, func(state appstructs.DownloadQueueState) bool {
		return state.Running == 1 && len(state.Items) == 1 && state.Items[0].Cancelable
	})
	runningID := running.Items[0].ID
	if runningID == "" {
		t.Fatalf("running item has empty id: %#v", running.Items[0])
	}

	if !CancelDownload(context.Background(), runningID) {
		t.Fatal("CancelDownload() = false, want true")
	}
	close(releaseAnalysis)

	state := waitForDownloadState(t, func(state appstructs.DownloadQueueState) bool {
		return state.Running == 0 && state.Pending == 0 && len(state.Items) == 1 && state.Items[0].Status == "canceled"
	})
	item := state.Items[0]
	if item.ID != runningID || !item.Retryable || item.Cancelable {
		t.Fatalf("canceled item = %#v", item)
	}
}

func TestRetryCanceledDependencyAnalysisUsesFreshQueueID(t *testing.T) {
	resetDownloadQueueForTest(t)
	downloadQueue.Lock()
	downloadQueue.nextID = 1
	downloadQueue.retryable = []retryableDownloadJob{{
		Status: "canceled",
		Reason: "canceled",
		Job: downloadJob{
			ID: "download-1",
			Version: models.ModVersion{
				ID:       "main-version",
				Platform: "modrinth",
				FileName: "main.jar",
			},
			Result: models.ModProject{
				Title: "Main",
			},
			TargetDir:        t.TempDir(),
			InstanceID:       "instance",
			MinecraftVersion: "1.21.1",
			ModLoader:        "fabric",
		},
	}}
	// Suppress the background runner; this test only verifies retry queue state.
	downloadQueue.running = true
	downloadQueue.Unlock()

	if !RetryDownload(nil, "download-1") {
		t.Fatal("RetryDownload() = false, want true")
	}

	state := GetDownloadQueueState()
	if state.Pending != 1 || len(state.Items) != 1 {
		t.Fatalf("state = %#v", state)
	}
	item := state.Items[0]
	if item.ID != "download-2" || item.Status != "pending" || !item.Cancelable || item.Retryable {
		t.Fatalf("item = %#v", item)
	}
}

func TestCancelPendingDownloadKeepsRetryableQueueItem(t *testing.T) {
	resetDownloadQueueForTest(t)
	downloadQueue.Lock()
	downloadQueue.nextID = 1
	downloadQueue.pending = []downloadJob{{
		ID: "download-1",
		Version: models.ModVersion{
			ID:       "version-1",
			Platform: "modrinth",
			FileName: "sodium.jar",
		},
		Result: models.ModProject{
			Title: "Sodium",
		},
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
	}}
	downloadQueue.Unlock()

	if !CancelDownload(nil, "download-1") {
		t.Fatal("CancelDownload() = false, want true")
	}

	state := GetDownloadQueueState()
	if !state.Active || state.Pending != 0 || state.Running != 0 {
		t.Fatalf("state summary = %#v", state)
	}
	if len(state.Items) != 1 {
		t.Fatalf("item count = %d, want 1", len(state.Items))
	}
	item := state.Items[0]
	if item.ID != "download-1" || item.Status != "canceled" || !item.Retryable || item.Cancelable {
		t.Fatalf("item = %#v", item)
	}
}

func TestRetryDownloadRequeuesRetryableItemWithFreshID(t *testing.T) {
	resetDownloadQueueForTest(t)
	downloadQueue.Lock()
	downloadQueue.nextID = 1
	downloadQueue.retryable = []retryableDownloadJob{{
		Status: "failed",
		Reason: "network error",
		Job: downloadJob{
			ID: "download-1",
			Version: models.ModVersion{
				ID:       "version-1",
				Platform: "modrinth",
				FileName: "sodium.jar",
			},
			Result: models.ModProject{
				Title: "Sodium",
			},
			TargetDir:        t.TempDir(),
			InstanceID:       "instance",
			MinecraftVersion: "1.21.1",
			ModLoader:        "fabric",
		},
	}}
	// Suppress the background runner; this test only needs to verify requeue state.
	downloadQueue.running = true
	downloadQueue.Unlock()

	if !RetryDownload(nil, "download-1") {
		t.Fatal("RetryDownload() = false, want true")
	}

	state := GetDownloadQueueState()
	if state.Pending != 1 || len(state.Items) != 1 {
		t.Fatalf("state = %#v", state)
	}
	item := state.Items[0]
	if item.ID != "download-2" || item.Status != "pending" || !item.Cancelable || item.Retryable {
		t.Fatalf("item = %#v", item)
	}
	if item.VersionID != "version-1" || item.MinecraftVersion != "1.21.1" || item.ModLoader != "fabric" {
		t.Fatalf("item lost retry context: %#v", item)
	}
}

func TestRunningDownloadIsRetryableAndMarksRestartRequest(t *testing.T) {
	resetDownloadQueueForTest(t)
	canceled := false
	downloadQueue.Lock()
	downloadQueue.nextID = 1
	downloadQueue.running = true
	downloadQueue.current = &downloadJob{
		ID: "download-1",
		Version: models.ModVersion{
			ID:       "version-1",
			Platform: "modrinth",
			FileName: "sodium.jar",
		},
		Result: models.ModProject{
			Title: "Sodium",
		},
		MinecraftVersion: "1.21.1",
		ModLoader:        "fabric",
		cancel: func() {
			canceled = true
		},
	}
	downloadQueue.Unlock()

	state := GetDownloadQueueState()
	if len(state.Items) != 1 || state.Items[0].Status != "running" || !state.Items[0].Retryable {
		t.Fatalf("running item = %#v", state.Items)
	}
	if !RetryDownload(nil, "download-1") {
		t.Fatal("RetryDownload() = false, want true")
	}
	if !canceled {
		t.Fatal("running retry did not cancel active attempt")
	}
	if !consumeDownloadRestartRequest("download-1") {
		t.Fatal("restart request was not recorded")
	}
	if consumeDownloadCancelRequest("download-1") {
		t.Fatal("running retry should not record a user-cancel request")
	}
}

func TestRetryStalledDownloadRequeuesUntilLimit(t *testing.T) {
	resetDownloadQueueForTest(t)
	downloadQueue.Lock()
	downloadQueue.running = true
	downloadQueue.nextID = 1
	downloadQueue.Unlock()

	job := downloadJob{
		ID: "download-1",
		Version: models.ModVersion{
			ID:       "version-1",
			Platform: "modrinth",
			FileName: "sodium.jar",
		},
		Result: models.ModProject{
			Title: "Sodium",
		},
		AutoAttempt: 0,
	}
	if !retryStalledDownload(nil, job, Events{}) {
		t.Fatal("retryStalledDownload() = false, want true")
	}

	state := GetDownloadQueueState()
	if state.Pending != 1 || len(state.Items) != 1 {
		t.Fatalf("state = %#v", state)
	}
	if state.Items[0].ID != "download-2" || state.Items[0].Status != "pending" {
		t.Fatalf("item = %#v", state.Items[0])
	}

	downloadQueue.Lock()
	requeuedAttempt := downloadQueue.pending[0].AutoAttempt
	downloadQueue.Unlock()
	if requeuedAttempt != 1 {
		t.Fatalf("AutoAttempt = %d, want 1", requeuedAttempt)
	}
	if retryStalledDownload(nil, downloadJob{AutoAttempt: maxDownloadStallRetries}, Events{}) {
		t.Fatal("retryStalledDownload() = true at retry limit, want false")
	}
}
