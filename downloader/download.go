package downloader

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/logging"
	"github.com/link-fgfgui/mod-downloader-core/minecraft"
	"github.com/link-fgfgui/mod-downloader-core/modbridge"
	"github.com/link-fgfgui/mod-downloader-core/models"
	"github.com/link-fgfgui/mod-downloader-core/providers"
	appstructs "github.com/link-fgfgui/mod-downloader-core/structs"

	"github.com/cavaliergopher/grab/v3"
)

type Events struct {
	OnQueueState     func(appstructs.DownloadQueueState)
	OnDownloadFailed func(appstructs.DownloadFailedEvent)
}

type downloadJob struct {
	ID                string
	Version           models.ModVersion
	Result            models.ModProject
	TargetDir         string
	InstanceID        string
	MinecraftVersion  string
	ModLoader         string
	CurseForgeAPIKey  string
	AutoAttempt       int
	IncompatiblePaths []global.LocalModFilePath
	cancel            context.CancelFunc
}

type retryableDownloadJob struct {
	Job    downloadJob
	Status string
	Reason string
}

type optionalDependencyReminderState struct {
	Snapshot appstructs.OptionalDependencyReminder
	Requests []appstructs.ModDownloadRequest
}

const (
	maxRetryableDownloadHistory = 20
	maxDownloadStallRetries     = 2
)

var (
	errDownloadStalled           = errors.New("download stalled: no byte progress")
	downloadStallTimeout         = 30 * time.Second
	downloadProgressPollInterval = time.Second
)

var downloadQueue = struct {
	sync.Mutex
	nextID            int64
	nextReminderID    int64
	pending           []downloadJob
	retryable         []retryableDownloadJob
	optionalReminders []optionalDependencyReminderState
	cancelRequested   map[string]struct{}
	restartRequested  map[string]struct{}
	running           bool
	current           *downloadJob
}{}

func QueueModDownload(ctx context.Context, req appstructs.ModDownloadRequest, curseForgeAPIKey string, events ...Events) appstructs.ModDownloadResult {
	return queueModDownload(ctx, req, curseForgeAPIKey, make(map[string]bool), firstEvents(events))
}

func queueModDownload(ctx context.Context, req appstructs.ModDownloadRequest, curseForgeAPIKey string, visited map[string]bool, events Events) appstructs.ModDownloadResult {
	req.MinecraftVersion = strings.TrimSpace(req.MinecraftVersion)
	req.ModLoader = strings.ToLower(strings.TrimSpace(req.ModLoader))
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		req.ProjectID = providers.ProjectReferenceFromSearchResult(req.Result)
	}

	req, instanceID, targetDir, ok := modbridge.ResolveInstallTarget(req)
	if !ok {
		return appstructs.ModDownloadResult{Skipped: true, Reason: "no install target"}
	}
	if req.ProjectID == "" || req.MinecraftVersion == "" || req.ModLoader == "" {
		return appstructs.ModDownloadResult{Skipped: true, Reason: "invalid request"}
	}

	version, ok := modbridge.ResolveVersion(req)
	if !ok {
		return appstructs.ModDownloadResult{Skipped: true, Reason: "no matching version"}
	}
	version = hydrateDownloadDependencies(req, version)
	if version.DownloadURL == "" {
		return appstructs.ModDownloadResult{Skipped: true, Reason: "missing download url"}
	}
	if visited == nil {
		visited = make(map[string]bool)
	}
	if key := projectVersionJobKey(version); key != "" {
		if visited[key] {
			return appstructs.ModDownloadResult{Skipped: true, Reason: "already queued"}
		}
		visited[key] = true
	}

	incompatiblePaths := incompatiblePathsFromConflicts(modbridge.IncompatibleConflicts(req), version.SHA1)
	queueMissingRequiredDependencies(ctx, req, version, instanceID, curseForgeAPIKey, visited, events)
	appendOptionalDependencyReminder(req, version, events)
	enqueueDownload(ctx, downloadJob{
		Version:           version,
		Result:            req.Result,
		TargetDir:         targetDir,
		InstanceID:        instanceID,
		MinecraftVersion:  req.MinecraftVersion,
		ModLoader:         req.ModLoader,
		CurseForgeAPIKey:  curseForgeAPIKey,
		IncompatiblePaths: incompatiblePaths,
	}, events)
	return appstructs.ModDownloadResult{
		Queued:    true,
		FileName:  version.FileName,
		VersionID: version.ID,
	}
}

func GetDownloadQueueState() appstructs.DownloadQueueState {
	return currentDownloadQueueState()
}

func CancelDownload(ctx context.Context, id string, events ...Events) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	eventSink := firstEvents(events)

	downloadQueue.Lock()
	for i, job := range downloadQueue.pending {
		if job.ID != id {
			continue
		}
		downloadQueue.pending = append(downloadQueue.pending[:i], downloadQueue.pending[i+1:]...)
		appendRetryableDownloadLocked(job, "canceled", "canceled")
		downloadQueue.Unlock()
		emitDownloadQueueState(eventSink)
		return true
	}

	var cancel context.CancelFunc
	if downloadQueue.current != nil && downloadQueue.current.ID == id {
		cancel = downloadQueue.current.cancel
		if downloadQueue.cancelRequested == nil {
			downloadQueue.cancelRequested = make(map[string]struct{})
		}
		downloadQueue.cancelRequested[id] = struct{}{}
	}
	downloadQueue.Unlock()

	if cancel == nil {
		return false
	}
	cancel()
	emitDownloadQueueState(eventSink)
	return true
}

func RetryDownload(ctx context.Context, id string, events ...Events) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	eventSink := firstEvents(events)

	downloadQueue.Lock()
	index := -1
	var job downloadJob
	for i, item := range downloadQueue.retryable {
		if item.Job.ID != id {
			continue
		}
		index = i
		job = item.Job
		break
	}
	if index >= 0 {
		downloadQueue.retryable = append(downloadQueue.retryable[:index], downloadQueue.retryable[index+1:]...)
		downloadQueue.Unlock()

		job.ID = ""
		job.cancel = nil
		job.AutoAttempt = 0
		enqueueDownload(ctx, job, eventSink)
		return true
	}

	var cancel context.CancelFunc
	if downloadQueue.current != nil && downloadQueue.current.ID == id {
		cancel = downloadQueue.current.cancel
		if cancel != nil {
			if downloadQueue.restartRequested == nil {
				downloadQueue.restartRequested = make(map[string]struct{})
			}
			downloadQueue.restartRequested[id] = struct{}{}
		}
	}
	downloadQueue.Unlock()

	if cancel == nil {
		return false
	}
	cancel()
	emitDownloadQueueState(eventSink)
	return true
}

func DismissOptionalDependencyReminder(id string, events ...Events) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	eventSink := firstEvents(events)
	downloadQueue.Lock()
	index := -1
	for i, reminder := range downloadQueue.optionalReminders {
		if reminder.Snapshot.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		downloadQueue.Unlock()
		return false
	}
	downloadQueue.optionalReminders = append(downloadQueue.optionalReminders[:index], downloadQueue.optionalReminders[index+1:]...)
	downloadQueue.Unlock()
	emitDownloadQueueState(eventSink)
	return true
}

func ClearOptionalDependencyReminders(events ...Events) bool {
	eventSink := firstEvents(events)
	downloadQueue.Lock()
	if len(downloadQueue.optionalReminders) == 0 {
		downloadQueue.Unlock()
		return false
	}
	downloadQueue.optionalReminders = nil
	downloadQueue.Unlock()
	emitDownloadQueueState(eventSink)
	return true
}

func InstallOptionalDependencies(ctx context.Context, id string, curseForgeAPIKey string, events ...Events) []appstructs.ModDownloadResult {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	eventSink := firstEvents(events)
	downloadQueue.Lock()
	index := -1
	var requests []appstructs.ModDownloadRequest
	for i, reminder := range downloadQueue.optionalReminders {
		if reminder.Snapshot.ID != id {
			continue
		}
		index = i
		requests = append(requests, reminder.Requests...)
		break
	}
	if index >= 0 {
		downloadQueue.optionalReminders = append(downloadQueue.optionalReminders[:index], downloadQueue.optionalReminders[index+1:]...)
	}
	downloadQueue.Unlock()
	if index < 0 {
		return nil
	}
	emitDownloadQueueState(eventSink)

	results := make([]appstructs.ModDownloadResult, 0, len(requests))
	visited := make(map[string]bool)
	for _, req := range requests {
		result := queueModDownload(ctx, req, curseForgeAPIKey, visited, eventSink)
		results = append(results, result)
	}
	return results
}

func AnalyzeBatchIncompatibleConflicts(req appstructs.BatchDownloadRequest) appstructs.BatchIncompatibleAnalysis {
	return modbridge.AnalyzeBatchIncompatibleConflicts(req)
}

func GetDownloadStates(req appstructs.DownloadStatesRequest, onBackfillComplete func()) []appstructs.ModDownloadButtonState {
	states := modbridge.DownloadStates(req, onBackfillComplete)
	queued := queuedProjectKeys()
	for i := range states {
		if queued[states[i].Key] {
			states[i].Loading = true
		}
	}
	return states
}

// queuedProjectKeys returns the set of project reference keys for all jobs
// currently in the download queue (both pending and running).
func queuedProjectKeys() map[string]bool {
	downloadQueue.Lock()
	defer downloadQueue.Unlock()
	keys := make(map[string]bool, len(downloadQueue.pending)+1)
	if downloadQueue.current != nil {
		if k := providers.ProjectReferenceFromSearchResult(downloadQueue.current.Result); k != "" {
			keys[k] = true
		}
	}
	for _, job := range downloadQueue.pending {
		if k := providers.ProjectReferenceFromSearchResult(job.Result); k != "" {
			keys[k] = true
		}
	}
	return keys
}

func projectVersionJobKey(version models.ModVersion) string {
	platform := strings.ToLower(strings.TrimSpace(version.Platform))
	projectID := strings.TrimSpace(version.ProjectID)
	versionID := strings.TrimSpace(version.ID)
	if platform == "" && projectID == "" && versionID == "" {
		return ""
	}
	return platform + ":" + projectID + ":" + versionID
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func enqueueDownload(ctx context.Context, job downloadJob, events Events) {
	downloadQueue.Lock()
	downloadQueue.nextID++
	job.ID = fmt.Sprintf("download-%d", downloadQueue.nextID)
	downloadQueue.pending = append(downloadQueue.pending, job)
	shouldStart := !downloadQueue.running
	if shouldStart {
		downloadQueue.running = true
	}
	downloadQueue.Unlock()

	emitDownloadQueueState(events)
	if shouldStart {
		go runDownloadQueue(ctx, events)
	}
}

func runDownloadQueue(ctx context.Context, events Events) {
	parentCtx := ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	for {
		downloadQueue.Lock()
		if len(downloadQueue.pending) == 0 {
			downloadQueue.running = false
			downloadQueue.Unlock()
			emitDownloadQueueState(events)
			return
		}
		job := downloadQueue.pending[0]
		downloadQueue.pending = downloadQueue.pending[1:]
		jobCtx, cancel := context.WithCancel(parentCtx)
		job.cancel = cancel
		runningJob := job
		downloadQueue.current = &runningJob
		downloadQueue.Unlock()
		emitDownloadQueueState(events)

		if err := downloadModJob(jobCtx, job); err != nil {
			if errors.Is(err, context.Canceled) {
				logging.Info("download mod canceled", "fileName", job.Version.FileName, "versionID", job.Version.ID, "targetDir", job.TargetDir)
				if consumeDownloadRestartRequest(job.ID) {
					job.AutoAttempt = 0
					enqueueDownload(parentCtx, job, events)
				} else if consumeDownloadCancelRequest(job.ID) {
					appendRetryableDownload(job, "canceled", "canceled")
				}
			} else if errors.Is(err, errDownloadStalled) && retryStalledDownload(parentCtx, job, events) {
				logging.Warn("download mod stalled, retrying", "fileName", job.Version.FileName, "versionID", job.Version.ID, "targetDir", job.TargetDir, "attempt", job.AutoAttempt+1, "maxRetries", maxDownloadStallRetries)
			} else {
				logging.Error("download mod failed", "fileName", job.Version.FileName, "versionID", job.Version.ID, "targetDir", job.TargetDir, "error", err)
				appendRetryableDownload(job, "failed", err.Error())
				emitDownloadFailed(events, job, err)
			}
		}
		cancel()
		downloadQueue.Lock()
		if downloadQueue.current != nil && downloadQueue.current.ID == job.ID {
			downloadQueue.current = nil
		}
		downloadQueue.Unlock()
		emitDownloadQueueState(events)
	}
}

func downloadModJob(ctx context.Context, job downloadJob) error {
	if err := os.MkdirAll(job.TargetDir, 0o755); err != nil {
		return err
	}
	archiveIncompatibleModJars(job.IncompatiblePaths)
	if tryHardlinkInstall(job) {
		return nil
	}
	// Try to get mod IDs from platform version (persisted or lazy-parsed via modbridge)
	modIDs := modbridge.VersionModIDs(job.Version, job.ModLoader)
	if len(modIDs) > 0 {
		existing := modbridge.LocalModPathsForModIDs(modIDs, job.InstanceID)
		if alreadyInstalled(existing, job.Version.SHA1) {
			return nil
		}
		return downloadModToTarget(ctx, job, existing)
	}

	return downloadModWithLocalParse(ctx, job)
}

// tryHardlinkInstall consults the SHA1→path hardlink index before downloading.
// On a hit with an existing source file it hardlinks (falling back to a no-overwrite
// copy) into the target location and fires a discard fetch in the background to
// preserve the mod author's download count.
// SHA1 is always available before download, so this works whether or not mod IDs
// are resolvable: with mod IDs it links straight to the final path; without them it
// links to a temp dir, parses locally, then moves to the final path.
func tryHardlinkInstall(job downloadJob) bool {
	srcPath, ok := global.HardlinkIndexLookup(job.Version.SHA1)
	if !ok {
		return false
	}
	finalPath := filepath.Join(job.TargetDir, filepath.Base(srcPath))
	if job.Version.FileName != "" {
		finalPath = filepath.Join(job.TargetDir, job.Version.FileName)
	}

	modIDs := modbridge.VersionModIDs(job.Version, job.ModLoader)
	var existing []global.LocalModFilePath
	if len(modIDs) > 0 {
		existing = modbridge.LocalModPathsForModIDs(modIDs, job.InstanceID)
		if alreadyInstalled(existing, job.Version.SHA1) {
			return true
		}
	}

	if downloadTargetExists(finalPath) && !pathInLocalModPaths(finalPath, existing) {
		logging.Info("hardlink skipped because target file already exists", "path", finalPath, "versionID", job.Version.ID)
		return true
	}
	archiveSupersededModJars(existing)
	if downloadTargetExists(finalPath) {
		logging.Info("hardlink skipped because target file already exists after archiving", "path", finalPath, "versionID", job.Version.ID)
		return true
	}

	if len(modIDs) > 0 {
		if err := minecraft.CreateHardLinkOrCopy(srcPath, finalPath); err != nil {
			logging.Warn("hardlink creation failed, falling back to download", "src", srcPath, "dst", finalPath, "error", err)
			return false
		}
		logging.Info("mod installed via hardlink/copy", "src", srcPath, "dst", finalPath, "versionID", job.Version.ID)
		go DiscardFetchFromNetwork(job.Version.DownloadURL, job.CurseForgeAPIKey)
		upsertDownloadedMod(finalPath, job)
		return true
	}

	tempDir := filepath.Join(filepath.Dir(job.TargetDir), ".mod-downloader-tmp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		logging.Warn("hardlink temp dir creation failed", "error", err)
		return false
	}
	tempPath := filepath.Join(tempDir, filepath.Base(finalPath))
	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		logging.Warn("hardlink temp cleanup failed", "path", tempPath, "error", err)
		return false
	}
	if err := minecraft.CreateHardLinkOrCopy(srcPath, tempPath); err != nil {
		logging.Warn("hardlink to temp failed, falling back to download", "src", srcPath, "dst", tempPath, "error", err)
		return false
	}
	sha1 := minecraft.FileSHA1(tempPath)
	parsedMods := minecraft.ParseModJarWithSHA1(tempPath, sha1, job.ModLoader)
	if len(parsedMods) == 0 {
		_ = os.Remove(tempPath)
		logging.Warn("hardlink source jar has no parseable metadata, falling back to download", "src", srcPath)
		return false
	}
	// Extract primary (strong-reference) mod IDs only. Mirrors downloadModWithLocalParse.
	parsedModIDs := minecraft.PrimaryModIDs(parsedMods)
	if len(parsedModIDs) > 0 && job.Version.ID != "" {
		_ = modbridge.PersistVersionModIDs(job.Version.ID, parsedModIDs)
	}
	parsedExisting := modbridge.LocalModPathsForModIDs(parsedModIDs, job.InstanceID)
	if alreadyInstalled(parsedExisting, sha1) {
		_ = os.Remove(tempPath)
		return true
	}
	archiveSupersededModJars(parsedExisting)
	if downloadTargetExists(finalPath) {
		logging.Info("hardlink skipped because target file already exists", "path", finalPath, "versionID", job.Version.ID)
		_ = os.Remove(tempPath)
		return true
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return false
	}
	logging.Info("mod installed via hardlink (local parse path)", "src", srcPath, "dst", finalPath, "versionID", job.Version.ID)
	go DiscardFetchFromNetwork(job.Version.DownloadURL, job.CurseForgeAPIKey)
	upsertDownloadedMod(finalPath, job)
	return true
}

// alreadyInstalled checks whether any existing file with the same modId already has the target SHA1.
func alreadyInstalled(existing []global.LocalModFilePath, latestSHA1 string) bool {
	latestSHA1 = strings.ToLower(strings.TrimSpace(latestSHA1))
	if latestSHA1 == "" {
		return false
	}
	for _, p := range existing {
		if strings.ToLower(strings.TrimSpace(p.FileSHA1)) == latestSHA1 {
			return true
		}
	}
	return false
}

func archiveSupersededModJars(paths []global.LocalModFilePath) {
	mcDir := global.GetMinecraftDir()
	for _, p := range paths {
		abs := p.Path
		if mcDir != "" && !filepath.IsAbs(abs) {
			abs = filepath.Join(mcDir, p.Path)
		}
		archivedPath := nextOldJarPath(abs)
		if err := os.Rename(abs, archivedPath); err != nil && !os.IsNotExist(err) {
			logging.Warn("archive superseded mod jar failed", "path", abs, "archivedPath", archivedPath, "error", err)
		} else {
			logging.Info("superseded mod jar archived", "path", abs, "archivedPath", archivedPath)
		}
		global.RemoveLocalModByPath(p.Path)
		global.HardlinkIndexRemove(abs)
	}
}

func archiveIncompatibleModJars(paths []global.LocalModFilePath) {
	if len(paths) == 0 {
		return
	}
	archiveSupersededModJars(paths)
}

func nextOldJarPath(path string) string {
	base := path + ".old"
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s.old.%d", path, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func queueMissingRequiredDependencies(ctx context.Context, req appstructs.ModDownloadRequest, version models.ModVersion, instanceID string, curseForgeAPIKey string, visited map[string]bool, events Events) {
	for _, dep := range version.Dependencies {
		if !isRequiredDependency(dep) {
			continue
		}

		depReq, ok := modbridge.DependencyDownloadRequest(version.Platform, dep, req)
		if !ok {
			logging.Warn("required dependency cannot be queued", "platform", version.Platform, "projectID", dep.DependencyProjectID, "versionID", dep.DependencyVersionID, "type", dep.DependencyType)
			continue
		}

		if modbridge.InstallStatusPrecise(depReq) != modbridge.BtnStatusNew {
			continue
		}
		result := queueModDownload(ctx, depReq, curseForgeAPIKey, visited, events)
		if result.Skipped {
			logging.Warn("required dependency queue skipped", "platform", depReq.Result.Platform, "projectID", depReq.ProjectID, "versionID", dep.DependencyVersionID, "reason", result.Reason)
		}
	}
}

func appendOptionalDependencyReminder(parent appstructs.ModDownloadRequest, version models.ModVersion, events Events) {
	candidates := make([]appstructs.OptionalDependencyCandidate, 0)
	requests := make([]appstructs.ModDownloadRequest, 0)
	for _, dep := range version.Dependencies {
		if !isOptionalDependency(dep) {
			continue
		}
		depReq, ok := modbridge.DependencyDownloadRequest(version.Platform, dep, parent)
		if !ok {
			logging.Warn("optional dependency cannot be resolved", "platform", version.Platform, "projectID", dep.DependencyProjectID, "versionID", dep.DependencyVersionID, "type", dep.DependencyType)
			continue
		}
		candidate := optionalDependencyCandidate(depReq)
		candidates = append(candidates, candidate)
		if !candidate.Disabled {
			requests = append(requests, depReq)
		}
	}
	if len(candidates) == 0 || len(requests) == 0 {
		return
	}

	mainKey := providers.ProjectReferenceFromSearchResult(parent.Result)
	if mainKey == "" {
		mainKey = strings.TrimSpace(parent.ProjectID)
	}
	snapshot := appstructs.OptionalDependencyReminder{
		MainProjectKey:   mainKey,
		MainTitle:        displayProjectTitle(parent.Result, mainKey),
		MainVersionID:    version.ID,
		MinecraftVersion: parent.MinecraftVersion,
		ModLoader:        parent.ModLoader,
		Dependencies:     candidates,
	}

	downloadQueue.Lock()
	for i, existing := range downloadQueue.optionalReminders {
		if existing.Snapshot.MainProjectKey == snapshot.MainProjectKey && existing.Snapshot.MainVersionID == snapshot.MainVersionID {
			snapshot.ID = existing.Snapshot.ID
			downloadQueue.optionalReminders[i] = optionalDependencyReminderState{Snapshot: snapshot, Requests: requests}
			downloadQueue.Unlock()
			emitDownloadQueueState(events)
			return
		}
	}
	downloadQueue.nextReminderID++
	snapshot.ID = fmt.Sprintf("optional-%d", downloadQueue.nextReminderID)
	downloadQueue.optionalReminders = append(downloadQueue.optionalReminders, optionalDependencyReminderState{Snapshot: snapshot, Requests: requests})
	downloadQueue.Unlock()
	emitDownloadQueueState(events)
}

func optionalDependencyCandidate(req appstructs.ModDownloadRequest) appstructs.OptionalDependencyCandidate {
	status := modbridge.InstallStatusPrecise(req)
	disabled := status == modbridge.BtnStatusInstalled
	reason := ""
	if _, ok := modbridge.ResolveVersion(req); !ok {
		disabled = true
		reason = "no matching version"
	}
	return appstructs.OptionalDependencyCandidate{
		ProjectKey: req.ProjectID,
		Platform:   req.Result.Platform,
		ProjectID:  req.Result.ProjectID,
		Title:      displayProjectTitle(req.Result, req.ProjectID),
		VersionID:  req.VersionID,
		Status:     status,
		Disabled:   disabled,
		Reason:     reason,
	}
}

func incompatiblePathsFromConflicts(conflicts []appstructs.IncompatibleConflict, targetSHA1 string) []global.LocalModFilePath {
	if len(conflicts) == 0 {
		return nil
	}
	targetSHA1 = strings.ToLower(strings.TrimSpace(targetSHA1))
	seen := make(map[string]struct{})
	out := make([]global.LocalModFilePath, 0)
	for _, conflict := range conflicts {
		for _, local := range conflict.Paths {
			if local.Path == "" {
				continue
			}
			if targetSHA1 != "" && strings.EqualFold(strings.TrimSpace(local.SHA1), targetSHA1) {
				continue
			}
			if _, dup := seen[local.Path]; dup {
				continue
			}
			seen[local.Path] = struct{}{}
			out = append(out, global.LocalModFilePath{
				ID:       local.Path,
				FileSHA1: local.SHA1,
				Path:     local.Path,
			})
		}
	}
	return out
}

func displayProjectTitle(project models.ModProject, fallback string) string {
	if title := strings.TrimSpace(project.Title); title != "" {
		return title
	}
	if slug := strings.TrimSpace(project.Slug); slug != "" {
		return slug
	}
	if projectID := strings.TrimSpace(project.ProjectID); projectID != "" {
		return projectID
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	return "Mod"
}

func hydrateDownloadDependencies(req appstructs.ModDownloadRequest, version models.ModVersion) models.ModVersion {
	if hasDownloadDependency(version.Dependencies) {
		return version
	}

	refreshed := providers.RefreshMatchingProjectVersions(req.Result, req.MinecraftVersion, req.ModLoader)
	if refreshedVersion, found := modbridge.FindVersionByID(refreshed, version.ID); found && hasDownloadDependency(refreshedVersion.Dependencies) {
		return refreshedVersion
	}
	return version
}

func hasDownloadDependency(deps []models.ModDependency) bool {
	for _, dep := range deps {
		if isRequiredDependency(dep) || isOptionalDependency(dep) || isIncompatibleDependency(dep) {
			return true
		}
	}
	return false
}

func isRequiredDependency(dep models.ModDependency) bool {
	return modbridge.IsDependencyType(dep, "required")
}

func isOptionalDependency(dep models.ModDependency) bool {
	return modbridge.IsDependencyType(dep, "optional")
}

func isIncompatibleDependency(dep models.ModDependency) bool {
	return modbridge.IsDependencyType(dep, "incompatible")
}

func downloadModToTarget(ctx context.Context, job downloadJob, existing []global.LocalModFilePath) error {
	tempDir := filepath.Join(filepath.Dir(job.TargetDir), ".mod-downloader-tmp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return err
	}

	client := grab.NewClient()
	req, err := grab.NewRequest(tempDir, job.Version.DownloadURL)
	if err != nil {
		return err
	}
	if job.Version.FileName != "" {
		req.Filename = filepath.Join(tempDir, job.Version.FileName)
	}
	req = req.WithContext(ctx)
	setCurseForgeAPIKeyHeader(req, job)

	resp, err := doDownloadWithStallTimeout(ctx, client, req)
	if err != nil {
		return err
	}
	if resp.Filename == "" {
		return nil
	}

	finalPath := filepath.Join(job.TargetDir, filepath.Base(resp.Filename))
	if downloadTargetExists(finalPath) && !pathInLocalModPaths(finalPath, existing) {
		logging.Info("download skipped because target file already exists", "path", finalPath, "versionID", job.Version.ID)
		_ = os.Remove(resp.Filename)
		return nil
	}
	archiveSupersededModJars(existing)
	if downloadTargetExists(finalPath) {
		logging.Info("download skipped because target file already exists after archiving superseded jars", "path", finalPath, "versionID", job.Version.ID)
		_ = os.Remove(resp.Filename)
		return nil
	}
	if err := os.Rename(resp.Filename, finalPath); err != nil {
		return err
	}
	upsertDownloadedMod(finalPath, job)
	return nil
}

func downloadModWithLocalParse(ctx context.Context, job downloadJob) error {
	tempDir := filepath.Join(filepath.Dir(job.TargetDir), ".mod-downloader-tmp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return err
	}

	client := grab.NewClient()
	req, err := grab.NewRequest(tempDir, job.Version.DownloadURL)
	if err != nil {
		return err
	}
	if job.Version.FileName != "" {
		req.Filename = filepath.Join(tempDir, job.Version.FileName)
	}
	req = req.WithContext(ctx)
	setCurseForgeAPIKeyHeader(req, job)

	resp, err := doDownloadWithStallTimeout(ctx, client, req)
	if err != nil {
		return err
	}
	if resp.Filename == "" {
		return nil
	}

	sha1 := minecraft.FileSHA1(resp.Filename)
	mods := minecraft.ParseModJarWithSHA1(resp.Filename, sha1, job.ModLoader)
	if len(mods) == 0 {
		_ = os.Remove(resp.Filename)
		return fmt.Errorf("downloaded jar has no parseable mod metadata: %s", filepath.Base(resp.Filename))
	}

	// Extract primary (strong-reference) mod IDs only. JIJ / nested-jar entries
	// are excluded: they must not participate in version persistence, conflict
	// detection, or replacement-archive decisions.
	modIDs := minecraft.PrimaryModIDs(mods)
	if len(modIDs) > 0 && job.Version.ID != "" {
		_ = modbridge.PersistVersionModIDs(job.Version.ID, modIDs)
	}

	existing := modbridge.LocalModPathsForModIDs(modIDs, job.InstanceID)
	if alreadyInstalled(existing, sha1) {
		_ = os.Remove(resp.Filename)
		return nil
	}
	archiveSupersededModJars(existing)

	finalPath := filepath.Join(job.TargetDir, filepath.Base(resp.Filename))
	if downloadTargetExists(finalPath) {
		logging.Info("download skipped because target file already exists", "path", finalPath, "versionID", job.Version.ID)
		_ = os.Remove(resp.Filename)
		return nil
	}
	if err := os.Rename(resp.Filename, finalPath); err != nil {
		return err
	}
	upsertDownloadedMod(finalPath, job)
	return nil
}

func downloadTargetExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return true
	} else if err != nil && !os.IsNotExist(err) {
		logging.Warn("download target stat failed", "path", path, "error", err)
	}
	return false
}

// isCurseForgeCDNURL checks if the URL is a CurseForge CDN download URL.
func isCurseForgeCDNURL(url string) bool {
	return strings.HasPrefix(url, "https://edge.forgecdn.net/files/")
}

func setCurseForgeAPIKeyHeader(req *grab.Request, job downloadJob) {
	if job.CurseForgeAPIKey != "" && isCurseForgeCDNURL(job.Version.DownloadURL) {
		req.HTTPRequest.Header.Set("x-api-key", job.CurseForgeAPIKey)
	}
}

func pathInLocalModPaths(path string, paths []global.LocalModFilePath) bool {
	path = filepath.Clean(path)
	mcDir := global.GetMinecraftDir()
	for _, p := range paths {
		existing := p.Path
		if mcDir != "" && !filepath.IsAbs(existing) {
			existing = filepath.Join(mcDir, existing)
		}
		if filepath.Clean(existing) == path {
			return true
		}
	}
	return false
}

func upsertDownloadedMod(path string, job downloadJob) {
	fileName := minecraft.StripJarSuffix(filepath.Base(path))
	sha1 := minecraft.FileSHA1(path)
	// Parse the downloaded JAR for mod metadata (pure local, no platform merging)
	r, err := zip.OpenReader(path)
	if err != nil {
		logging.Warn("parse downloaded jar failed", "path", path, "error", err)
		return
	}
	defer r.Close()
	mods := minecraft.ParseModZipReader(&r.Reader, filepath.Base(path), job.ModLoader)
	if len(mods) == 0 {
		logging.Warn("downloaded jar has no parseable mod metadata", "path", path)
		return
	}

	// Store local JAR metadata enriched with install-time platform display fields.
	if strings.TrimSpace(job.Result.ProjectID) != "" && strings.TrimSpace(job.Result.Title) != "" {
		if err := providers.StoreProject(job.Result); err != nil {
			logging.Warn("store downloaded mod project metadata failed", "platform", job.Result.Platform, "projectID", job.Result.ProjectID, "error", err)
		}
	}
	mods = modbridge.ApplyProjectMetadataToModInfos(mods, job.Result)
	global.SetJarMetadata(sha1, mods)

	relPath := path
	if mcDir := global.GetMinecraftDir(); mcDir != "" {
		if rel, err := filepath.Rel(mcDir, path); err == nil {
			relPath = rel
		}
	}
	absPath := path
	if !filepath.IsAbs(absPath) {
		if mcDir := global.GetMinecraftDir(); mcDir != "" {
			absPath = filepath.Join(mcDir, path)
		}
	}
	global.HardlinkIndexAdd(sha1, absPath)
	for i := range mods {
		mods[i].FileName = fileName
		mods[i].Path = relPath
		mods[i].SHA1 = sha1
		mods[i].Enabled = true
		global.UpsertLocalMod(mods[i], job.InstanceID, job.MinecraftVersion, job.ModLoader)
	}
}

func currentDownloadQueueState() appstructs.DownloadQueueState {
	downloadQueue.Lock()
	defer downloadQueue.Unlock()
	running := 0
	if downloadQueue.current != nil {
		running = 1
	}
	items := make([]appstructs.DownloadQueueItem, 0, running+len(downloadQueue.pending)+len(downloadQueue.retryable))
	if downloadQueue.current != nil {
		item := downloadQueueItemFromJob(*downloadQueue.current, "running", true)
		item.Retryable = true
		items = append(items, item)
	}
	for _, job := range downloadQueue.pending {
		items = append(items, downloadQueueItemFromJob(job, "pending", true))
	}
	for _, item := range downloadQueue.retryable {
		queueItem := downloadQueueItemFromJob(item.Job, item.Status, false)
		queueItem.Retryable = true
		queueItem.Reason = item.Reason
		items = append(items, queueItem)
	}
	reminders := make([]appstructs.OptionalDependencyReminder, 0, len(downloadQueue.optionalReminders))
	for _, reminder := range downloadQueue.optionalReminders {
		reminders = append(reminders, reminder.Snapshot)
	}
	return appstructs.DownloadQueueState{
		Active:            running > 0 || len(downloadQueue.pending) > 0 || len(downloadQueue.retryable) > 0,
		Pending:           len(downloadQueue.pending),
		Running:           running,
		MessageCount:      len(reminders),
		Items:             items,
		OptionalReminders: reminders,
	}
}

func downloadQueueItemFromJob(job downloadJob, status string, cancelable bool) appstructs.DownloadQueueItem {
	title := strings.TrimSpace(job.Result.Title)
	if title == "" {
		title = firstNonEmpty(job.Version.Name, job.Version.FileName, job.Version.Version, job.Version.ID)
	}
	return appstructs.DownloadQueueItem{
		ID:               job.ID,
		Status:           status,
		Title:            title,
		FileName:         job.Version.FileName,
		VersionID:        job.Version.ID,
		Platform:         job.Version.Platform,
		MinecraftVersion: job.MinecraftVersion,
		ModLoader:        job.ModLoader,
		Cancelable:       cancelable,
	}
}

func appendRetryableDownload(job downloadJob, status string, reason string) {
	downloadQueue.Lock()
	defer downloadQueue.Unlock()
	appendRetryableDownloadLocked(job, status, reason)
}

func appendRetryableDownloadLocked(job downloadJob, status string, reason string) {
	job.cancel = nil
	downloadQueue.retryable = append(downloadQueue.retryable, retryableDownloadJob{
		Job:    job,
		Status: strings.TrimSpace(status),
		Reason: strings.TrimSpace(reason),
	})
	if overflow := len(downloadQueue.retryable) - maxRetryableDownloadHistory; overflow > 0 {
		downloadQueue.retryable = append([]retryableDownloadJob(nil), downloadQueue.retryable[overflow:]...)
	}
}

func consumeDownloadCancelRequest(id string) bool {
	downloadQueue.Lock()
	defer downloadQueue.Unlock()
	if downloadQueue.cancelRequested == nil {
		return false
	}
	if _, ok := downloadQueue.cancelRequested[id]; !ok {
		return false
	}
	delete(downloadQueue.cancelRequested, id)
	return true
}

func consumeDownloadRestartRequest(id string) bool {
	downloadQueue.Lock()
	defer downloadQueue.Unlock()
	if downloadQueue.restartRequested == nil {
		return false
	}
	if _, ok := downloadQueue.restartRequested[id]; !ok {
		return false
	}
	delete(downloadQueue.restartRequested, id)
	return true
}

func retryStalledDownload(ctx context.Context, job downloadJob, events Events) bool {
	if job.AutoAttempt >= maxDownloadStallRetries {
		return false
	}
	job.ID = ""
	job.cancel = nil
	job.AutoAttempt++
	enqueueDownload(ctx, job, events)
	return true
}

func doDownloadWithStallTimeout(ctx context.Context, client *grab.Client, req *grab.Request) (*grab.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req = req.WithContext(attemptCtx)
	resp := client.Do(req)
	if err := waitForDownloadWithStallTimeout(resp, cancel, downloadStallTimeout, downloadProgressPollInterval); err != nil {
		return resp, err
	}
	return resp, nil
}

func waitForDownloadWithStallTimeout(resp *grab.Response, cancel context.CancelFunc, stallTimeout time.Duration, pollInterval time.Duration) error {
	if stallTimeout <= 0 {
		return resp.Err()
	}
	if pollInterval <= 0 {
		pollInterval = time.Second
	}

	lastBytes := resp.BytesComplete()
	lastProgress := time.Now()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-resp.Done:
			return resp.Err()
		case <-ticker.C:
			currentBytes := resp.BytesComplete()
			if currentBytes > lastBytes {
				lastBytes = currentBytes
				lastProgress = time.Now()
				continue
			}
			if time.Since(lastProgress) >= stallTimeout {
				cancel()
				_ = resp.Err()
				return errDownloadStalled
			}
		}
	}
}

func firstEvents(events []Events) Events {
	if len(events) == 0 {
		return Events{}
	}
	return events[0]
}

func emitDownloadQueueState(events Events) {
	if events.OnQueueState == nil {
		return
	}
	events.OnQueueState(currentDownloadQueueState())
}

func emitDownloadFailed(events Events, job downloadJob, err error) {
	if events.OnDownloadFailed == nil || err == nil {
		return
	}
	events.OnDownloadFailed(appstructs.DownloadFailedEvent{
		FileName:  job.Version.FileName,
		VersionID: job.Version.ID,
		Reason:    err.Error(),
	})
}
