package source

type Chunker interface {
	ID() string
	Chunk(content []byte) ([]Passage, error)
}

type Passage struct {
	Content  string `json:"content"`
	Location string `json:"location"`
}
