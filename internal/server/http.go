package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-consensus/ac/internal/api"
	agentcontext "github.com/agent-consensus/ac/internal/context"
)

type Server struct {
	store  *FileStore
	token  string
	webDir string
}

type Options struct {
	DataPath string
	Token    string
	WebDir   string
}

func Open(dataPath, token string) (*Server, error) {
	return OpenWithOptions(Options{DataPath: dataPath, Token: token})
}

func OpenWithOptions(options Options) (*Server, error) {
	store, err := OpenFileStore(options.DataPath)
	if err != nil {
		return nil, err
	}
	return NewWithWebDir(store, options.Token, options.WebDir), nil
}

func New(store *FileStore, token string) *Server {
	return NewWithWebDir(store, token, "")
}

func NewWithWebDir(store *FileStore, token, webDir string) *Server {
	return &Server{store: store, token: strings.TrimSpace(token), webDir: strings.TrimSpace(webDir)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.Handle("GET /api/v1/organizations", s.requireAuth(http.HandlerFunc(s.handleListOrganizations)))
	mux.Handle("POST /api/v1/organizations", s.requireAuth(http.HandlerFunc(s.handleCreateOrganization)))
	mux.Handle("GET /api/v1/organizations/{organization}", s.requireAuth(http.HandlerFunc(s.handleGetOrganization)))
	mux.Handle("DELETE /api/v1/organizations/{organization}/repositories/{repository}/decisions/{decision}", s.requireAuth(http.HandlerFunc(s.handleDeleteRepositoryDecision)))
	mux.Handle("GET /api/v1/organizations/{organization}/snapshot", s.requireAuth(http.HandlerFunc(s.handleSnapshot)))
	mux.Handle("POST /api/v1/organizations/{organization}/sync", s.requireAuth(http.HandlerFunc(s.handleSync)))
	mux.Handle("POST /api/v1/organizations/{organization}/events", s.requireAuth(http.HandlerFunc(s.handleSubmitEvent)))
	mux.Handle("POST /api/v1/organizations/{organization}/proposals", s.requireAuth(http.HandlerFunc(s.handleSubmitProposal)))
	mux.Handle("PATCH /api/v1/organizations/{organization}/proposals/{proposal}", s.requireAuth(http.HandlerFunc(s.handleReviewProposal)))
	mux.Handle("POST /api/v1/organizations/{organization}/promotions", s.requireAuth(http.HandlerFunc(s.handleSubmitPromotion)))
	mux.Handle("PATCH /api/v1/organizations/{organization}/promotions/{promotion}", s.requireAuth(http.HandlerFunc(s.handleReviewPromotion)))
	mux.Handle("GET /api/v1/organizations/{organization}/context", s.requireAuth(http.HandlerFunc(s.handleContext)))
	mux.Handle("GET /api/v1/organizations/{organization}/context-records", s.requireAuth(http.HandlerFunc(s.handleListContextRecords)))
	mux.Handle("POST /api/v1/organizations/{organization}/context-records", s.requireAuth(http.HandlerFunc(s.handleRecordContext)))

	mux.HandleFunc("GET /assets/{asset...}", s.handleAsset)
	mux.HandleFunc("GET /", s.handleDashboard)
	return s.securityHeaders(mux)
}

func (s *Server) ListenAndServe(address string) error {
	server := &http.Server{Addr: address, Handler: s.Handler()}
	return server.ListenAndServe()
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, api.HealthResponse{
		Status:       "ok",
		APIVersion:   api.Version,
		AuthRequired: s.token != "",
	})
}

func (s *Server) handleListOrganizations(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, s.store.ListOrganizations())
}

func (s *Server) handleCreateOrganization(writer http.ResponseWriter, request *http.Request) {
	var input api.CreateOrganizationRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	organization, err := s.store.CreateOrganization(input.Name)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, organization)
}

func (s *Server) handleGetOrganization(writer http.ResponseWriter, request *http.Request) {
	detail, err := s.store.GetOrganization(request.PathValue("organization"))
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func (s *Server) handleSnapshot(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := s.store.Snapshot(request.PathValue("organization"), request.URL.Query().Get("repository"))
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (s *Server) handleSubmitPromotion(writer http.ResponseWriter, request *http.Request) {
	var input api.SubmitPromotionRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	promotion, err := s.store.SubmitPromotion(request.PathValue("organization"), input)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, promotion)
}

func (s *Server) handleSubmitEvent(writer http.ResponseWriter, request *http.Request) {
	var input api.SubmitEventRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	event, err := s.store.SubmitEvent(request.PathValue("organization"), input)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, event)
}

func (s *Server) handleSubmitProposal(writer http.ResponseWriter, request *http.Request) {
	var input api.SubmitProposalRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	proposal, err := s.store.SubmitProposal(request.PathValue("organization"), input)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, proposal)
}

func (s *Server) handleReviewProposal(writer http.ResponseWriter, request *http.Request) {
	var input api.ReviewProposalRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	proposal, err := s.store.ReviewProposal(request.PathValue("organization"), request.PathValue("proposal"), input)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, proposal)
}

func (s *Server) handleReviewPromotion(writer http.ResponseWriter, request *http.Request) {
	var input api.ReviewPromotionRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	promotion, err := s.store.ReviewPromotion(request.PathValue("organization"), request.PathValue("promotion"), input)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, promotion)
}

func (s *Server) handleSync(writer http.ResponseWriter, request *http.Request) {
	var input api.SyncRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	result, err := s.store.Sync(request.PathValue("organization"), input)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleContext(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	document, err := s.store.BuildContext(
		request.PathValue("organization"),
		query.Get("repository"),
		query.Get("role"),
		query.Get("agent"),
	)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	// format=markdown renders the exact fragment agents receive on injection,
	// using the same renderer as the CLI's `agc context --format markdown`.
	if strings.EqualFold(query.Get("format"), "markdown") {
		writer.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, agentcontext.Markdown(document))
		return
	}
	writeJSON(writer, http.StatusOK, document)
}

func (s *Server) handleDeleteRepositoryDecision(writer http.ResponseWriter, request *http.Request) {
	err := s.store.DeleteRepositoryDecision(
		request.PathValue("organization"),
		request.PathValue("repository"),
		request.PathValue("decision"),
	)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListContextRecords(writer http.ResponseWriter, request *http.Request) {
	detail, err := s.store.GetOrganization(request.PathValue("organization"))
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail.ContextRecords)
}

func (s *Server) handleRecordContext(writer http.ResponseWriter, request *http.Request) {
	var input api.ContextRecordInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	record, err := s.store.RecordContext(request.PathValue("organization"), input)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, record)
}

func (s *Server) handleDashboard(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	if s.webDir == "" {
		serveWebBuildRequired(writer)
		return
	}
	indexPath := filepath.Join(s.webDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		serveWebBuildRequired(writer)
		return
	}
	writer.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(writer, request, indexPath)
}

func (s *Server) handleAsset(writer http.ResponseWriter, request *http.Request) {
	asset := strings.TrimPrefix(request.PathValue("asset"), "/")
	if s.webDir == "" || !fs.ValidPath(asset) {
		http.NotFound(writer, request)
		return
	}
	assetPath := filepath.Join(s.webDir, "assets", filepath.FromSlash(asset))
	if info, err := os.Stat(assetPath); err != nil || info.IsDir() {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(writer, request, assetPath)
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.token == "" {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeAPIError(writer, http.StatusUnauthorized, "valid bearer token required")
			return
		}
		provided := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			writeAPIError(writer, http.StatusUnauthorized, "valid bearer token required")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Frame-Options", "DENY")
		// React is intentionally emitted as a single self-contained index.html.
		// Inline script and style are limited to this local dashboard document.
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(writer, request)
	})
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid JSON request: "+err.Error())
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(writer, http.StatusBadRequest, "request must contain one JSON document")
		return false
	}
	return true
}

func writeStoreError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeAPIError(writer, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrAlreadyExists), errors.Is(err, ErrConflict):
		writeAPIError(writer, http.StatusConflict, err.Error())
	default:
		writeAPIError(writer, http.StatusBadRequest, err.Error())
	}
}

func writeAPIError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, api.ErrorResponse{Error: message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func serveWebBuildRequired(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(writer, "AGC dashboard is not built. Run npm run build in the web directory, then start agc server.\n")
}
