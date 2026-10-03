package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/timurcravtov/walrus/internal/recommend"
)

// Recommend endpoints.
//
//	POST /v1/recommenders/{recommender}/recommend   v2: a named recommender with its seed
//	GET  /v1/recommend/{user}                       v1: the `default` recommender, seed user
//	POST /v1/recommend/{user}                       v1: the same, with an exclude list
//	GET  /v1/recommendations/{rec_id}/explain/{item} explain one item of one exact list
//
// Authentication is the admin key for now: tenants and scoped keys (the `recommend` scope)
// are not built yet, so there is one tenant and one key.

const maxRecommendBody = 1 << 20

func (s *Server) routeRecommend() {
	s.mux.HandleFunc("POST /v1/recommenders/{recommender}/recommend", s.requireAdmin(s.recommendNamed))
	s.mux.HandleFunc("GET /v1/recommend/{user}", s.requireAdmin(s.recommendV1))
	s.mux.HandleFunc("POST /v1/recommend/{user}", s.requireAdmin(s.recommendV1))
	s.mux.HandleFunc("GET /v1/recommendations/{rec_id}/explain/{item}", s.requireAdmin(s.explainRec))
}

func (s *Server) recommendNamed(w http.ResponseWriter, r *http.Request) {
	var req recommend.Request
	if !decodeBody(w, r, &req) {
		return
	}
	req.Recommender = r.PathValue("recommender")
	s.runRecommend(w, r, req)
}

// recommendV1 serves the v1 routes: the path names a user, the recommender is `default`.
func (s *Server) recommendV1(w http.ResponseWriter, r *http.Request) {
	req := recommend.Request{Recommender: "default"}
	if r.Method == http.MethodPost {
		if !decodeBody(w, r, &req) {
			return
		}
		req.Recommender = "default"
	} else {
		q := r.URL.Query()
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "validation_error", "limit must be a whole number")
				return
			}
			req.Limit = n
		}
		req.Preset = q.Get("preset")
		for key, vals := range q {
			id, ok := strings.CutPrefix(key, "knobs.")
			if !ok || len(vals) == 0 {
				continue
			}
			f, err := strconv.ParseFloat(vals[0], 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "validation_error", key+" must be a number")
				return
			}
			if req.Knobs == nil {
				req.Knobs = map[string]float64{}
			}
			req.Knobs[id] = f
		}
	}
	req.User = r.PathValue("user")
	s.runRecommend(w, r, req)
}

func (s *Server) runRecommend(w http.ResponseWriter, r *http.Request, req recommend.Request) {
	resp, err := s.rec.Recommend(r.Context(), req)
	if err != nil {
		writeRecommendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) explainRec(w http.ResponseWriter, r *http.Request) {
	b, err := s.rec.ExplainRec(r.PathValue("rec_id"), r.PathValue("item"))
	if err != nil {
		writeRecommendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// decodeBody reads a JSON body, rejecting unknown fields; an empty body is an empty request, which
// is valid for a recommender with no seed.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRecommendBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "validation_error", "the request body is larger than 1 MiB")
			return false
		}
		writeError(w, http.StatusBadRequest, "validation_error", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeRecommendError(w http.ResponseWriter, err error) {
	var e *recommend.Error
	if errors.As(err, &e) {
		writeError(w, e.Status, e.Code, e.Message)
		return
	}
	slog.Error("recommend", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
