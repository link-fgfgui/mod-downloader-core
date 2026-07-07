package structs

type LocalModBatchOperationRequest struct {
	Paths  []string `json:"paths"`
	Action string   `json:"action"`
}

type UnusedDependencyScanRequest struct {
	ExcludedPaths []string `json:"excludedPaths,omitempty"`
}

type UnusedDependencyCandidate struct {
	Path       string   `json:"path"`
	FileName   string   `json:"fileName"`
	ModIDs     []string `json:"modIds"`
	Name       string   `json:"name,omitempty"`
	OnlineName string   `json:"onlineName,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
}

type UnusedDependencyScanResult struct {
	Candidates []UnusedDependencyCandidate `json:"candidates"`
}
