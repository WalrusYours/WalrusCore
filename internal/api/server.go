// Package api is the HTTP layer: routing, authentication and JSON. Handlers stay thin and
// call the services.
package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/timurcravtov/walrus/internal/ingest"
	"github.com/timurcravtov/walrus/internal/rank"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

const sessionCookie = "walrus_session"

type Config struct {
	AdminKey     string
	InstanceID   string
	InstanceName string
	Version      string
	// MaxSchemaBytes caps a pushed schema. Zero means 1 MiB.
	MaxSchemaBytes int64
	// FailDelay slows a wrong sign-in attempt. Zero means 250 ms; negative disables it.
	FailDelay  time.Duration
	SessionTTL time.Duration
}

type Server struct {
	cfg      Config
	schema   *schema.Service
	rec      *recommend.Service
	ingest   *ingest.Service
	store    store.Store
	sessions *sessions
	mux      *http.ServeMux
}

// Option configures the server beyond its required parts.
type Option func(*options)

type options struct {
	ranker   recommend.Ranker
	profiles recommend.Profiles
	store    store.Store
}

// WithRanker sets what generates candidates and scores them. Without it the recommend endpoints
// answer with an empty list, because nothing has been ingested.
func WithRanker(r recommend.Ranker) Option { return func(o *options) { o.ranker = r } }

// WithProfiles supplies users' saved knob values.
func WithProfiles(p recommend.Profiles) Option { return func(o *options) { o.profiles = p } }

// WithStore sets where entities are kept. Without it they are kept in memory.
func WithStore(st store.Store) Option { return func(o *options) { o.store = st } }

func New(cfg Config, svc *schema.Service, opts ...Option) http.Handler {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if cfg.MaxSchemaBytes == 0 {
		cfg.MaxSchemaBytes = 1 << 20
	}
	if cfg.FailDelay == 0 {
		cfg.FailDelay = 250 * time.Millisecond
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	if o.store == nil {
		o.store = memory.New()
	}
	if o.ranker == nil {
		o.ranker = rank.New(o.store)
	}
	rec := recommend.NewService(svc, o.ranker)
	if o.profiles != nil {
		rec.WithProfiles(o.profiles)
	}
	s := &Server{cfg: cfg, schema: svc, rec: rec, ingest: ingest.NewService(svc, o.store), store: o.store, sessions: newSessions(cfg.SessionTTL), mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /health", s.health)
	s.mux.HandleFunc("GET /v1/health", s.health)

	s.mux.HandleFunc("POST /v1/admin/session", s.login)
	s.mux.HandleFunc("GET /v1/admin/session", s.requireAdmin(s.sessionStatus))
	s.mux.HandleFunc("DELETE /v1/admin/session", s.logout)

	s.mux.HandleFunc("PUT /v1/schema", s.requireAdmin(s.putSchema))
	s.mux.HandleFunc("GET /v1/schema", s.requireAdmin(s.getSchema))
	s.mux.HandleFunc("GET /v1/schema/history", s.requireAdmin(s.schemaHistory))

	s.routeEntities()
	s.routeKnobs()
	s.routeRecommend()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(rec, r)
	slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func equalKeys(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func (s *Server) isAdmin(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return equalKeys(strings.TrimPrefix(h, "Bearer "), s.cfg.AdminKey)
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return s.sessions.valid(c.Value)
	}
	return false
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sign in or send Authorization: Bearer <admin key>")
			return
		}
		next(w, r)
	}
}

func isSecure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// ---- handlers ----

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":        "ok",
		"version":       s.cfg.Version,
		"instance_id":   s.cfg.InstanceID,
		"instance_name": s.cfg.InstanceName,
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", `send {"key": "<admin key>"}`)
		return
	}
	if !equalKeys(body.Key, s.cfg.AdminKey) {
		if s.cfg.FailDelay > 0 {
			time.Sleep(s.cfg.FailDelay)
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "wrong key")
		return
	}
	token, expires := s.sessions.create()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isSecure(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sessionStatus(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func queryBool(r *http.Request, name string) bool {
	switch strings.ToLower(r.URL.Query().Get(name)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func (s *Server) putSchema(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxSchemaBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "schema exceeds the size limit")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "could not read the request body")
		return
	}
	res := s.schema.Load(body, schema.LoadOptions{
		DryRun:          queryBool(r, "dry_run"),
		ConfirmBreaking: queryBool(r, "confirm_breaking"),
		Author:          "admin",
	})
	status := http.StatusOK
	switch {
	case res.NeedsConfirm:
		status = http.StatusConflict
	case !res.OK:
		status = http.StatusBadRequest
	}
	writeJSON(w, status, res)
}

func (s *Server) getSchema(w http.ResponseWriter, _ *http.Request) {
	v, ok := s.schema.Current()
	if !ok {
		writeError(w, http.StatusNotFound, "no_schema", "no schema has been pushed yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"yaml": v.YAML, "version": v.Version, "hash": v.Hash})
}

func (s *Server) schemaHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.schema.History())
}
