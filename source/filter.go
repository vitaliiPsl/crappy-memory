package source

const DefaultPageSize = 100

type Filter struct {
	Scopes  []string `json:"scopes,omitempty"`
	IDs     []string `json:"ids,omitempty"`
	AfterID string   `json:"after_id,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

type Page struct {
	Documents []Document `json:"documents"`
	NextID    string     `json:"next_id,omitempty"`
}

type ChunkFilter struct {
	DocumentIDs []string `json:"document_ids,omitempty"`
	IDs         []string `json:"ids,omitempty"`
	AfterID     string   `json:"after_id,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

type ChunkPage struct {
	Chunks []Chunk `json:"chunks"`
	NextID string  `json:"next_id,omitempty"`
}
