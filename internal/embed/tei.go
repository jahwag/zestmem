package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type TEI struct {
	url    string
	client *http.Client
}

func NewTEI(url string, timeout time.Duration) *TEI {
	return &TEI{url: strings.TrimRight(url, "/"), client: &http.Client{Timeout: timeout}}
}

func (t *TEI) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]any{"inputs": text, "normalize": true, "truncate": true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TEI embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("TEI embed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	var vectors [][]float32
	if err := json.NewDecoder(resp.Body).Decode(&vectors); err != nil {
		return nil, fmt.Errorf("TEI embed response: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != 384 {
		return nil, fmt.Errorf("TEI embed: expected one 384-dimensional vector, got %d vectors", len(vectors))
	}
	return vectors[0], nil
}

func (t *TEI) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("TEI health: HTTP %d", resp.StatusCode)
	}
	return nil
}
