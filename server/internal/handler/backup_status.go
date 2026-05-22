package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type BackupManifest struct {
	CompletedAt string `json:"completed_at"`
	Files       struct {
		PostgresDump int64 `json:"postgres.dump"`
	} `json:"files"`
}

type BackupStatusResponse struct {
	Configured       bool    `json:"configured"`
	LastBackupAt     *string `json:"last_backup_at"`
	AgeSeconds       *int64  `json:"age_seconds"`
	Healthy          bool    `json:"healthy"`
	PostgresDumpSize *int64  `json:"postgres_dump_size"`
	Error            *string `json:"error,omitempty"`
}

const staleThresholdSeconds = 36 * 3600 // 36 hours

func (h *Handler) GetBackupStatus(w http.ResponseWriter, r *http.Request) {
	// Owner/admin only - ops-sensitive data.
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	dir := os.Getenv("BACKUP_STATUS_DIR")
	if dir == "" {
		writeJSON(w, http.StatusOK, BackupStatusResponse{Configured: false, Healthy: false})
		return
	}

	manifestPath := filepath.Join(dir, "latest", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		msg := "manifest not found - backup may never have run or volume not mounted"
		writeJSON(w, http.StatusOK, BackupStatusResponse{
			Configured: true,
			Healthy:    false,
			Error:      &msg,
		})
		return
	}

	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		msg := "manifest parse error"
		writeJSON(w, http.StatusOK, BackupStatusResponse{
			Configured: true,
			Healthy:    false,
			Error:      &msg,
		})
		return
	}

	t, err := time.Parse(time.RFC3339, manifest.CompletedAt)
	if err != nil {
		msg := "manifest completed_at parse error"
		writeJSON(w, http.StatusOK, BackupStatusResponse{
			Configured: true,
			Healthy:    false,
			Error:      &msg,
		})
		return
	}

	age := int64(time.Since(t).Seconds())
	healthy := age < staleThresholdSeconds
	at := manifest.CompletedAt
	size := manifest.Files.PostgresDump
	writeJSON(w, http.StatusOK, BackupStatusResponse{
		Configured:       true,
		LastBackupAt:     &at,
		AgeSeconds:       &age,
		Healthy:          healthy,
		PostgresDumpSize: &size,
	})
}
