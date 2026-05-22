package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	mcpDirectoryAPIURL       = "https://mcp.directory/api/v1/servers"
	mcpDirectoryHomepageBase = "https://mcp.directory/servers/"
	mcpDirectoryPageLimit    = 100
)

type MCPDirectoryEntryResponse struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Slug           string   `json:"slug"`
	Description    *string  `json:"description"`
	TransportTypes []string `json:"transport_types"`
	PublisherName  *string  `json:"publisher_name"`
	Homepage       *string  `json:"homepage"`
	Stars          int32    `json:"stars"`
	LastFetchedAt  string   `json:"last_fetched_at"`
}

type mcpDirectoryAPIResponse struct {
	Servers []mcpDirectoryAPIServer `json:"servers"`
	Total   int                     `json:"total"`
	Limit   int                     `json:"limit"`
	Offset  int                     `json:"offset"`
}

type mcpDirectoryAPIServer struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Slug             string                `json:"slug"`
	ShortDescription string                `json:"shortDescription"`
	TransportType    []string              `json:"transportType"`
	Stars            int32                 `json:"stars"`
	Publisher        mcpDirectoryPublisher `json:"publisher"`
}

type mcpDirectoryPublisher struct {
	Name string `json:"name"`
}

func mapMCPDirectoryTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "streamable-http":
		return "http"
	case "stdio":
		return "stdio"
	case "sse":
		return "sse"
	default:
		return strings.TrimSpace(value)
	}
}

func mapMCPDirectoryTransports(values []string) []string {
	seen := map[string]bool{}
	mapped := make([]string, 0, len(values))
	for _, value := range values {
		transport := mapMCPDirectoryTransport(value)
		if transport == "" || seen[transport] {
			continue
		}
		seen[transport] = true
		mapped = append(mapped, transport)
	}
	return mapped
}

func (h *Handler) RefreshMCPServerDirectory(ctx context.Context) (int, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	offset := 0
	fetched := 0

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, mcpDirectoryAPIURL, nil)
		if err != nil {
			return fetched, err
		}
		query := req.URL.Query()
		query.Set("limit", strconv.Itoa(mcpDirectoryPageLimit))
		query.Set("offset", strconv.Itoa(offset))
		req.URL.RawQuery = query.Encode()

		resp, err := client.Do(req)
		if err != nil {
			slog.Error("mcp directory refresh request failed", "offset", offset, "error", err)
			return fetched, err
		}

		var payload mcpDirectoryAPIResponse
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			err := fmt.Errorf("mcp.directory returned status %d", resp.StatusCode)
			slog.Error("mcp directory refresh request failed", "offset", offset, "error", err)
			return fetched, err
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			slog.Error("mcp directory refresh decode failed", "offset", offset, "error", err)
			return fetched, err
		}
		resp.Body.Close()

		for _, entry := range payload.Servers {
			if entry.ID == "" || entry.Name == "" || entry.Slug == "" {
				continue
			}
			_, err := h.Queries.UpsertMCPServerDirectoryEntry(ctx, db.UpsertMCPServerDirectoryEntryParams{
				ID:             entry.ID,
				Name:           entry.Name,
				Slug:           entry.Slug,
				Description:    strToNullableText(entry.ShortDescription),
				TransportTypes: mapMCPDirectoryTransports(entry.TransportType),
				PublisherName:  strToNullableText(entry.Publisher.Name),
				Homepage:       strToNullableText(mcpDirectoryHomepageBase + entry.Slug),
				Stars:          entry.Stars,
			})
			if err != nil {
				slog.Error("mcp directory upsert failed", "id", entry.ID, "error", err)
				return fetched, err
			}
			fetched++
		}

		offset += mcpDirectoryPageLimit
		if payload.Total == 0 || offset >= payload.Total {
			break
		}
		select {
		case <-ctx.Done():
			return fetched, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}

	slog.Info("mcp directory refresh complete", "fetched", fetched)
	return fetched, nil
}

func strToNullableText(value string) pgtype.Text {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: trimmed, Valid: true}
}

func mcpDirectoryEntryToResponse(row db.SearchMCPServerDirectoryRow) MCPDirectoryEntryResponse {
	return MCPDirectoryEntryResponse{
		ID:             row.ID,
		Name:           row.Name,
		Slug:           row.Slug,
		Description:    textToPtr(row.Description),
		TransportTypes: row.TransportTypes,
		PublisherName:  textToPtr(row.PublisherName),
		Homepage:       textToPtr(row.Homepage),
		Stars:          row.Stars,
		LastFetchedAt:  timestampToString(row.LastFetchedAt),
	}
}

func (h *Handler) SearchMCPServerDirectory(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	limit := parseBoundedInt(params.Get("limit"), 24, 1, 100)
	offset := parseBoundedInt(params.Get("offset"), 0, 0, 1_000_000)
	query := strings.TrimSpace(params.Get("q"))

	var transport pgtype.Text
	if value := strings.TrimSpace(params.Get("transport")); value != "" {
		transport = pgtype.Text{String: value, Valid: true}
	}

	rows, err := h.Queries.SearchMCPServerDirectory(r.Context(), db.SearchMCPServerDirectoryParams{
		Limit:     int32(limit),
		Offset:    int32(offset),
		Query:     query,
		Transport: transport,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to search mcp server directory")
		return
	}

	entries := make([]MCPDirectoryEntryResponse, 0, len(rows))
	total := int32(0)
	for i, row := range rows {
		if i == 0 {
			total = row.TotalCount
		}
		entries = append(entries, mcpDirectoryEntryToResponse(row))
	}

	lastFetchedAt, err := h.Queries.GetMCPServerDirectoryLastFetchedAt(r.Context())
	if err != nil && err != pgx.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "failed to load mcp server directory freshness")
		return
	}

	var freshness *string
	if lastFetchedAt.Valid {
		value := timestampToString(lastFetchedAt)
		freshness = &value
	} else {
		h.refreshMCPServerDirectoryInBackground()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries":         entries,
		"total":           total,
		"last_fetched_at": freshness,
	})
}

func (h *Handler) TriggerMCPServerDirectoryRefresh(w http.ResponseWriter, r *http.Request) {
	if !h.requireMCPServerWriteAccess(w, r) {
		return
	}
	h.refreshMCPServerDirectoryInBackground()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "refresh_started"})
}

func (h *Handler) refreshMCPServerDirectoryInBackground() {
	ctx := h.ServiceCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		if _, err := h.RefreshMCPServerDirectory(ctx); err != nil {
			slog.Error("mcp directory background refresh failed", "error", err)
		}
	}()
}

func (h *Handler) RunMCPServerDirectoryRefresher(ctx context.Context) {
	refreshIfStale := func() {
		lastFetchedAt, err := h.Queries.GetMCPServerDirectoryLastFetchedAt(ctx)
		if err != nil && err != pgx.ErrNoRows {
			slog.Error("mcp directory freshness check failed", "error", err)
			return
		}
		if lastFetchedAt.Valid && time.Since(lastFetchedAt.Time) < 24*time.Hour {
			return
		}
		if _, err := h.RefreshMCPServerDirectory(ctx); err != nil {
			slog.Error("mcp directory scheduled refresh failed", "error", err)
		}
	}

	refreshIfStale()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshIfStale()
		}
	}
}

func parseBoundedInt(raw string, fallback, minValue, maxValue int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
