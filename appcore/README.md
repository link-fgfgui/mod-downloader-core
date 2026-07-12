# `appcore` Service Map

`appcore` is the adapter-neutral orchestration layer. It owns the service
object, lifecycle/configuration, and workflows exposed to both Wails and the
CLI. It must not import Wails runtime packages.

## Where To Start

- `contracts.go`: event taxonomy, service options, and typed request/response
  contracts shared by adapters.
- `../models/models.go`: canonical `ModProject`/`ModVersion` definitions used
  by the service; this is the only model file in the package.
- `service.go`: lifecycle and the public workflow methods. Search this file by
  the method groups below instead of reading it linearly.
- `dependency_impact.go`, `unused_dependencies.go`: local dependency analysis.
- `packwiz_export.go`: packwiz export orchestration; native dialogs stay in the
  root Wails adapter.
- `user_agent.go`: versioned outbound identity.

`service.go` method groups are: search/pinning (roughly lines 158-237),
favorites (255-630), settings (634-850), downloads (850-935), local instances
(942-1382), and runtime/configuration/event plumbing (1399 onward). Line
numbers are orientation only; use the function names as the stable anchors.

## Contracts

- Return typed `models` and `structs` values; do not add aliases or re-exports.
- Emit `appcore.Event` for UI progress; adapters decide how to display it.
- Keep persistence and provider access behind their existing packages.
- Keep cancellation checks inside the downloader queue lifecycle.
