// Package web provides the web UI and API for viewing request logs.
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"anthropic-proxy/internal/storage"
)

// Handler serves the web UI and API endpoints.
type Handler struct {
	store *storage.Storage
}

// NewHandler creates a new web handler.
func NewHandler(store *storage.Storage) *Handler {
	return &Handler{store: store}
}

// RegisterRoutes registers all web routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/web", h.serveIndex)
	mux.HandleFunc("/api/logs", h.apiLogs)
	mux.HandleFunc("/api/logs/", h.apiLogDetail)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("templates/index.html")
	if err != nil {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

type logsResponse struct {
	Logs []storage.RequestLog `json:"logs"`
}

func (h *Handler) apiLogs(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 30
	}
	offset := (page - 1) * limit

	logs, err := h.store.GetLogs(limit, offset)
	if err != nil {
		slog.Error("get logs failed", "err", err)
		http.Error(w, "failed to get logs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logsResponse{Logs: logs})
}

func (h *Handler) apiLogDetail(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid log id", http.StatusBadRequest)
		return
	}

	log, err := h.store.GetLogByID(id)
	if err != nil {
		http.Error(w, "log not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(log)
}
