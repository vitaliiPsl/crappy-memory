package source

import "time"

type Document struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	MediaType string    `json:"media_type"`
	Content   []byte    `json:"content"`
	Hash      string    `json:"hash"`
	Chunker   string    `json:"chunker"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Chunk struct {
	ID         string `json:"id"`
	DocumentID string `json:"document_id"`
	Scope      string `json:"scope"`
	Position   int    `json:"position"`
	Location   string `json:"location"`
	Content    string `json:"content"`
}
