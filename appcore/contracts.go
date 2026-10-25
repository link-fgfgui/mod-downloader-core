package appcore

import (
	"context"
	"sync"

	"github.com/link-fgfgui/mod-downloader-core/configs"
	"github.com/link-fgfgui/mod-downloader-core/models"
	"github.com/link-fgfgui/mod-downloader-core/storage"
	appstructs "github.com/link-fgfgui/mod-downloader-core/structs"
)

// EventKind is the adapter-neutral event taxonomy emitted by Service.
type EventKind string

const (
	EventSearchModsUpdated      EventKind = "searchModsUpdated"
	EventDownloadStatesUpdated  EventKind = "downloadStatesUpdated"
	EventDownloadQueueUpdated   EventKind = "downloadQueueUpdated"
	EventDownloadFailed         EventKind = "downloadFailed"
	EventDownloadCompleted      EventKind = "downloadCompleted"
	EventMinecraftDirChanged    EventKind = "minecraftDirChanged"
	EventSelectedVersionChanged EventKind = "selectedVersionChanged"
	EventUsageStatsUpdated      EventKind = "usageStatsUpdated"
)

type Event struct {
	Kind    EventKind
	Payload any
}

type Options struct {
	Config                *configs.Config
	ConfigOverrides       ConfigOverrides
	Runtime               RuntimeOptions
	Version               string
	LoadMinecraftReleases bool
	OnEvent               func(Event)
}

type RuntimeOptions struct {
	WorkDir          string
	TargetModsDir    string
	MinecraftVersion string
	ModLoader        string
	CacheDir         string
	CachePath        string
	DefaultCacheDir  string
	NoConfigFile     bool
}

type ConfigOverrides struct {
	MinecraftDir        string
	CurseForgeAPIKey    string
	ModrinthAPIKey      string
	HasMinecraftDir     bool
	HasCurseForgeAPIKey bool
	HasModrinthAPIKey   bool
}

type FavoriteBulkAddRequest struct {
	TargetListIDs []string              `json:"targetListIds"`
	Mods          []storage.FavoriteMod `json:"mods"`
}

type FavoriteListCopyRequest struct {
	SourceListID string `json:"sourceListId"`
	TargetListID string `json:"targetListId"`
}

type FavoriteBulkOperationResult struct {
	Added   int      `json:"added"`
	Updated int      `json:"updated"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors,omitempty"`
}

type FavoriteCrossVersionCopyRequest struct {
	SourceListID     string `json:"sourceListId"`
	MinecraftVersion string `json:"minecraftVersion"`
	ModLoader        string `json:"modLoader"`
	IgnoreConflicts  bool   `json:"ignoreConflicts,omitempty"`
}

type FavoriteCrossVersionCopyPreview struct {
	SourceListID     string                             `json:"sourceListId"`
	TargetListName   string                             `json:"targetListName"`
	MinecraftVersion string                             `json:"minecraftVersion"`
	ModLoader        string                             `json:"modLoader"`
	NameConflict     bool                               `json:"nameConflict"`
	Matched          []FavoriteCrossVersionCopyMatch    `json:"matched"`
	Conflicts        []FavoriteCrossVersionCopyConflict `json:"conflicts"`
	Errors           []string                           `json:"errors,omitempty"`
}

type FavoriteCrossVersionCopyMatch struct {
	Source  storage.FavoriteModEntry `json:"source"`
	Target  storage.FavoriteMod      `json:"target"`
	Project models.ModProject        `json:"project"`
	Version models.ModVersion        `json:"version"`
}

type FavoriteCrossVersionCopyConflict struct {
	Source storage.FavoriteModEntry `json:"source"`
	Reason string                   `json:"reason"`
}

type FavoriteCrossVersionCopyApplyResult struct {
	Applied    bool                            `json:"applied"`
	TargetList storage.FavoriteList            `json:"targetList"`
	Preview    FavoriteCrossVersionCopyPreview `json:"preview"`
	Result     FavoriteBulkOperationResult     `json:"result"`
}

type InstallWaitResult struct {
	Result appstructs.ModDownloadResult     `json:"result"`
	State  appstructs.DownloadQueueState    `json:"state"`
	Errors []appstructs.DownloadFailedEvent `json:"errors,omitempty"`
}

type InstallToDirRequest struct {
	ProjectID        string            `json:"projectId"`
	Project          models.ModProject `json:"project"`
	TargetDir        string            `json:"targetDir"`
	MinecraftVersion string            `json:"minecraftVersion"`
	ModLoader        string            `json:"modLoader"`
	VersionID        string            `json:"versionId,omitempty"`
}

// Service is the adapter-neutral workflow coordinator shared by Wails and CLI.
type Service struct {
	ctx             context.Context
	config          *configs.Config
	options         Options
	modsWatchMu     sync.Mutex
	modsWatchCancel context.CancelFunc
	modsWatchDir    string
}
