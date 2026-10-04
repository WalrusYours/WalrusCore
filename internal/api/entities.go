package api

import (
	"errors"
	"net/http"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/ingest"
	"github.com/timurcravtov/walrus/internal/store"
)

const maxImportBytes = 64 << 20

func (s *Server) routeEntities() {
	s.mux.HandleFunc("POST /v1/entities", s.requireAdmin(s.postEntities))
	s.mux.HandleFunc("GET /v1/entities", s.requireAdmin(s.countEntities))
	s.mux.HandleFunc("GET /v1/entities/{type}/{id}", s.requireAdmin(s.getEntity))
	s.mux.HandleFunc("PUT /v1/entities/{type}/{id}", s.requireAdmin(s.putEntity))
	s.mux.HandleFunc("POST /v1/import", s.requireAdmin(s.importEntities))
}

// putEntity creates or replaces one entity: 201 when it is new, 200 when it replaced one.
func (s *Server) putEntity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Attributes map[string]any `json:"attributes"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	typ, id := r.PathValue("type"), r.PathValue("id")
	_, err := s.store.Entity(r.Context(), typ, domain.EntityID(id))
	existed := err == nil
	if err := s.ingest.One(r.Context(), typ, id, body.Attributes); err != nil {
		writeIngestError(w, err)
		return
	}
	e, err := s.store.Entity(r.Context(), typ, domain.EntityID(id))
	if err != nil {
		writeRecommendError(w, err)
		return
	}
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeJSON(w, status, entityView(e))
}

// importEntities reads JSON Lines, one entity per line, for loading a whole catalogue.
func (s *Server) importEntities(w http.ResponseWriter, r *http.Request) {
	res, err := s.ingest.Import(r.Context(), http.MaxBytesReader(w, r.Body, maxImportBytes))
	if err != nil {
		writeIngestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeIngestError(w http.ResponseWriter, err error) {
	var e *ingest.Error
	if errors.As(err, &e) {
		writeError(w, e.Status, e.Code, e.Message)
		return
	}
	writeRecommendError(w, err)
}

// postEntities answers 200 even when some entities are rejected: the rejected list says which.
func (s *Server) postEntities(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Entities []ingest.Raw `json:"entities"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	res, err := s.ingest.Entities(r.Context(), body.Entities)
	if err != nil {
		writeIngestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) countEntities(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.CountEntities(r.Context())
	if err != nil {
		writeRecommendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	e, err := s.store.Entity(r.Context(), r.PathValue("type"), domain.EntityID(r.PathValue("id")))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "unknown_entity", "no such entity")
		return
	}
	if err != nil {
		writeRecommendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entityView(e))
}

func entityView(e domain.Entity) any {
	return struct {
		Entity     string                  `json:"entity"`
		ID         string                  `json:"id"`
		Attributes map[string]domain.Value `json:"attributes"`
	}{e.Type, string(e.ID), e.Attrs}
}
