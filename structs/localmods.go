package structs

type LocalModBatchOperationRequest struct {
	Paths  []string `json:"paths"`
	Action string   `json:"action"`
}
