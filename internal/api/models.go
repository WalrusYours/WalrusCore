package api

import (
	"errors"
	"net/http"

	"github.com/timurcravtov/walrus/internal/learn"
)

// Model endpoints: the learned part of the `embedding` signal.
//
//	GET  /v1/models        every embedding signal's model against what the schema asks for, and the last run
//	POST /v1/models/train  start a training run in the background (?signal=<id> for one signal)
//
// Training is a control-path action, so both need the admin key. A run can take longer than a
// request may, so the POST answers 202 at once and the GET shows how it went.

func (s *Server) routeModels() {
	s.mux.HandleFunc("GET /v1/models", s.requireAdmin(s.getModels))
	s.mux.HandleFunc("POST /v1/models/train", s.requireAdmin(s.trainModels))
}

func (s *Server) getModels(w http.ResponseWriter, r *http.Request) {
	c := s.schema.Compiled()
	if c == nil {
		writeError(w, http.StatusNotFound, "no_schema", "no schema has been pushed yet")
		return
	}
	models, err := learn.Status(r.Context(), s.store, c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not read the models")
		return
	}
	if models == nil {
		models = []learn.State{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "job": s.trainer.Job()})
}

func (s *Server) trainModels(w http.ResponseWriter, r *http.Request) {
	var signals []string
	if id := r.URL.Query().Get("signal"); id != "" {
		signals = []string{id}
	}
	err := s.trainer.Start("api", signals...)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"job": s.trainer.Job()})
	case errors.Is(err, learn.ErrRunning):
		writeError(w, http.StatusConflict, "already_running", "a training run is already in progress; read it at GET /v1/models")
	case errors.Is(err, learn.ErrNoSchema):
		writeError(w, http.StatusNotFound, "no_schema", "no schema has been pushed yet")
	case errors.Is(err, learn.ErrNothingToTrain):
		writeError(w, http.StatusUnprocessableEntity, "nothing_to_train", "the schema declares no embedding signal")
	case errors.Is(err, learn.ErrUnknownSignal):
		writeError(w, http.StatusNotFound, "unknown_signal", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", "could not start training")
	}
}
