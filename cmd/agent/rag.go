package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/google/uuid"
)

// ragSearchClient — реализация runtime.RAGClient: ходит в rag-svc POST /v1/search
// с internal-JWT. Без него движок использует noopRAG и агент не видит базу знаний.
type ragSearchClient struct {
	url         string
	jwtFactory  func() (string, error)
	client      *http.Client
	embProvider string
}

func newRAGClient(ragURL string, jwtFactory func() (string, error)) *ragSearchClient {
	return &ragSearchClient{
		url:        ragURL,
		jwtFactory: jwtFactory,
		client:     &http.Client{Timeout: 30 * time.Second},
		// Должен совпадать с провайдером, которым индексировались чанки (дефолт платформы — bge_m3).
		embProvider: envOr("RAG_EMBEDDING_PROVIDER", "bge_m3"),
	}
}

func (c *ragSearchClient) Search(ctx context.Context, req runtime.RAGSearchRequest) ([]runtime.RAGChunk, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}
	body, _ := json.Marshal(map[string]any{
		"profile_id":         req.ProfileID.String(),
		"agent_id":           req.AgentID.String(),
		"query":              req.Query,
		"top_k":              topK,
		"min_score":          req.MinScore,
		"embedding_provider": c.embProvider,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/v1/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token, terr := c.jwtFactory(); terr == nil {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("rag /v1/search status %d", resp.StatusCode)
	}

	var out struct {
		Chunks []struct {
			SourceID uuid.UUID `json:"source_id"`
			Content  string    `json:"content"`
			Score    float32   `json:"score"`
			Title    string    `json:"title"`
		} `json:"chunks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}

	chunks := make([]runtime.RAGChunk, 0, len(out.Chunks))
	for _, ch := range out.Chunks {
		chunks = append(chunks, runtime.RAGChunk{
			Content: ch.Content,
			Source:  runtime.RAGSource{ID: ch.SourceID, Title: ch.Title},
			Score:   ch.Score,
		})
	}
	return chunks, nil
}
