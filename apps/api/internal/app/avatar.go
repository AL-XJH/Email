package app

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxAvatarBytes = 2 << 20 // 2 MiB

func (a *App) avatarPath(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" || strings.ContainsAny(userID, `/\`) || userID == "." || userID == ".." {
		return ""
	}
	return filepath.Join(a.config().DataDir, "avatars", userID)
}

func (a *App) userAvatarURL(userID string) string {
	path := a.avatarPath(userID)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	return "/api/me/avatar?v=" + info.ModTime().UTC().Format("20060102150405.000000000")
}

func (a *App) handleGetAvatar(w http.ResponseWriter, r *http.Request) {
	path := a.avatarPath(currentUser(r).ID)
	if path == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		respondError(w, http.StatusInternalServerError, "failed to read avatar")
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read avatar")
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "avatar", info.ModTime(), bytes.NewReader(data))
}

func (a *App) handleUploadAvatar(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+1<<20)
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		badRequest(w, errors.New("invalid or oversized image"))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		badRequest(w, errors.New("image file is required"))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxAvatarBytes {
		badRequest(w, errors.New("image must be under 2 MB"))
		return
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		badRequest(w, errors.New("avatar must be a JPEG, PNG, GIF or WebP image"))
		return
	}
	path := a.avatarPath(currentUser(r).ID)
	if path == "" {
		respondError(w, http.StatusInternalServerError, "invalid avatar path")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create avatar directory")
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".avatar-*")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store avatar")
		return
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		respondError(w, http.StatusInternalServerError, "failed to store avatar")
		return
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		respondError(w, http.StatusInternalServerError, "failed to store avatar")
		return
	}
	if err := tmp.Close(); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store avatar")
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store avatar")
		return
	}
	cleanup = false
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "avatarUrl": a.userAvatarURL(currentUser(r).ID)})
}

func (a *App) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	path := a.avatarPath(currentUser(r).ID)
	if path == "" {
		respondJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		respondError(w, http.StatusInternalServerError, "failed to remove avatar")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
