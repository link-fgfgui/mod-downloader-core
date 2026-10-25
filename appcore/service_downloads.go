package appcore

import (
	"context"
	"time"

	"github.com/link-fgfgui/mod-downloader-core/downloader"
	"github.com/link-fgfgui/mod-downloader-core/storage"
	appstructs "github.com/link-fgfgui/mod-downloader-core/structs"
)

func (s *Service) QueueModDownload(req appstructs.ModDownloadRequest) appstructs.ModDownloadResult {
	req = s.applyRuntimeDownloadRequest(req)
	return downloader.QueueModDownloadWithUserAgent(s.ctx, req, s.effectiveCurseforgeAPIKey(), s.UserAgent(), s.downloadEvents())
}

func (s *Service) InstallModAndWait(ctx context.Context, req appstructs.ModDownloadRequest) InstallWaitResult {
	req = s.applyRuntimeDownloadRequest(req)
	var failed []appstructs.DownloadFailedEvent
	events := s.downloadEvents()
	previousFailed := events.OnDownloadFailed
	events.OnDownloadFailed = func(event appstructs.DownloadFailedEvent) {
		failed = append(failed, event)
		if previousFailed != nil {
			previousFailed(event)
		}
	}

	result := downloader.QueueModDownloadWithUserAgent(ctx, req, s.effectiveCurseforgeAPIKey(), s.UserAgent(), events)
	if !result.Queued {
		return InstallWaitResult{Result: result, State: downloader.GetDownloadQueueState()}
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := downloader.GetDownloadQueueState()
		if !state.Active {
			return InstallWaitResult{Result: result, State: state, Errors: failed}
		}
		select {
		case <-ctx.Done():
			return InstallWaitResult{
				Result: appstructs.ModDownloadResult{Skipped: true, Reason: ctx.Err().Error()},
				State:  state,
				Errors: failed,
			}
		case <-ticker.C:
		}
	}
}

func (s *Service) InstallModToDirAndWait(ctx context.Context, req InstallToDirRequest) InstallWaitResult {
	return s.InstallModAndWait(ctx, appstructs.ModDownloadRequest{
		ProjectID:        req.ProjectID,
		Result:           req.Project,
		MinecraftVersion: req.MinecraftVersion,
		ModLoader:        req.ModLoader,
		VersionID:        req.VersionID,
		TargetDir:        req.TargetDir,
	})
}

func (s *Service) GetDownloadQueueState() appstructs.DownloadQueueState {
	return downloader.GetDownloadQueueState()
}

func (s *Service) GetUsageStats() storage.UsageStats {
	return storage.GetUsageStats()
}

func (s *Service) CancelDownload(id string) bool {
	return downloader.CancelDownload(s.ctx, id, s.downloadEvents())
}

func (s *Service) RetryDownload(id string) bool {
	return downloader.RetryDownload(s.ctx, id, s.downloadEvents())
}

func (s *Service) RemoveDownload(id string) bool {
	return downloader.RemoveDownload(id, s.downloadEvents())
}

func (s *Service) DismissOptionalDependencyReminder(id string) bool {
	return downloader.DismissOptionalDependencyReminder(id, s.downloadEvents())
}

func (s *Service) ClearOptionalDependencyReminders() bool {
	return downloader.ClearOptionalDependencyReminders(s.downloadEvents())
}

func (s *Service) InstallOptionalDependencies(id string) []appstructs.ModDownloadResult {
	return downloader.InstallOptionalDependenciesWithUserAgent(s.ctx, id, s.effectiveCurseforgeAPIKey(), s.UserAgent(), s.downloadEvents())
}

func (s *Service) AnalyzeBatchIncompatibleConflicts(req appstructs.BatchDownloadRequest) appstructs.BatchIncompatibleAnalysis {
	req = s.applyRuntimeBatchDownloadRequest(req)
	return downloader.AnalyzeBatchIncompatibleConflicts(req)
}

func (s *Service) GetDownloadStates(req appstructs.DownloadStatesRequest) []appstructs.ModDownloadButtonState {
	req = s.applyRuntimeDownloadStatesRequest(req)
	return downloader.GetDownloadStates(req, func() {
		s.emit(EventDownloadStatesUpdated, nil)
	})
}
