package api

import (
	"context"
	"fmt"
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

// readyTTL is how long one upstream probe's answer is reused.
const readyTTL = 30 * time.Second

// Models is the OpenAI list shape new-api reads for "fetch models". A model
// that cannot be served is not advertised: with MAGPIE_FETCH_MODE=off there
// is no deepseek-fetch.
func (h *Handlers) Models(w http.ResponseWriter, r *http.Request) {
	data := []modelEntry{{ID: "deepseek-search", Object: "model", OwnedBy: "magpie-search-adapter"}}
	if h.FetchEnabled {
		data = append(data, modelEntry{ID: "deepseek-fetch", Object: "model", OwnedBy: "magpie-search-adapter"})
	}
	writeJSON(w, http.StatusOK, modelList{Object: "list", Data: data})
}

// Health answers without touching Magpie.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mode": h.Mode})
}

// Ready checks Magpie, cached for readyTTL. The probe is detached from the
// caller's cancellation so a client disconnecting cannot cache a failure that
// Magpie never had, and one probe runs at a time so a burst on this
// unauthenticated route cannot turn into a burst of upstream probes.
func (h *Handlers) Ready(w http.ResponseWriter, r *http.Request) {
	if h.ReadyFn == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true})
		return
	}
	h.readyMu.Lock()
	if !h.readyAt.IsZero() && time.Since(h.readyAt) < readyTTL {
		err := h.readyErr
		h.readyMu.Unlock()
		writeReady(w, err)
		return
	}
	if h.probing {
		done := h.probeDone
		h.readyMu.Unlock()
		select {
		case <-done:
		case <-r.Context().Done():
			return
		}
		h.readyMu.Lock()
		err := h.readyErr
		h.readyMu.Unlock()
		writeReady(w, err)
		return
	}
	h.probing = true
	h.probeDone = make(chan struct{})
	done := h.probeDone
	h.readyMu.Unlock()

	writeReady(w, h.probe(r.Context(), done))
}

// probe runs one upstream check and publishes its outcome, including when the
// probe panics, so a caller waiting on done is never left hanging.
func (h *Handlers) probe(callerCtx context.Context, done chan struct{}) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("ready probe panicked: %v", recovered)
			h.publishProbe(done, err)
			panic(recovered)
		}
		h.publishProbe(done, err)
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(callerCtx), 15*time.Second)
	defer cancel()
	return h.ReadyFn(ctx)
}

func (h *Handlers) publishProbe(done chan struct{}, err error) {
	h.readyMu.Lock()
	h.readyAt = time.Now()
	h.readyErr = err
	h.probing = false
	close(done)
	h.readyMu.Unlock()
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
