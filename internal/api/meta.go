package api

import (
	"context"
	"net/http"
	"time"
)

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type modelList struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

// Models is the OpenAI list shape new-api reads for "fetch models".
func (h *Handlers) Models(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, modelList{
		Object: "list",
		Data: []modelEntry{
			{ID: "deepseek-search", Object: "model", OwnedBy: "magpie-search-adapter"},
			{ID: "deepseek-fetch", Object: "model", OwnedBy: "magpie-search-adapter"},
		},
	})
}

// Health answers without touching Magpie.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mode": h.Mode})
}

// Ready checks Magpie, cached for 30 seconds. The probe is detached from
// the caller's cancellation so a client disconnecting cannot cache a
// failure that Magpie never had.
func (h *Handlers) Ready(w http.ResponseWriter, r *http.Request) {
	if h.ReadyFn == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true})
		return
	}
	h.readyMu.Lock()
	if !h.readyAt.IsZero() && time.Since(h.readyAt) < 30*time.Second {
		err := h.readyErr
		h.readyMu.Unlock()
		writeReady(w, err)
		return
	}
	h.readyMu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	err := h.ReadyFn(ctx)
	cancel()

	h.readyMu.Lock()
	h.readyAt = time.Now()
	h.readyErr = err
	h.readyMu.Unlock()
	writeReady(w, err)
}

func writeReady(w http.ResponseWriter, err error) {
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ready": false,
			"error": redact(err.Error()),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true})
}
