// Package tools exposes the two deterministic tools that do something Pi's own
// toolset cannot (S7): deliver a file from the vault to the owner's chat and ask
// the owner to choose between options. Pi writes the vault with its own `bash`,
// `edit` and `write`, so this server has no note-writing endpoint at all.
//
// The server listens on loopback only and requires the per-process token
// generated at construction in the X-Alter-Token header, so no other local
// process can drive the owner's chat. Business failures are 4xx with a JSON
// {"error": "..."} body: the Pi tool returns that text to the model so it can
// explain the problem instead of retrying blindly.
package tools

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/verdu/alter/internal/vault"
)

// tokenHeader carries the per-process shared secret on every request.
const tokenHeader = "X-Alter-Token"

// DefaultAskTimeout bounds how long ask_options waits for the owner's press. A
// turn must never hang on a button nobody taps.
const DefaultAskTimeout = 10 * time.Minute

// noAnswer is the answer reported when the owner does not choose an option. It
// is a normal result, not an error: the model can keep talking.
const noAnswer = "sin respuesta"

// maxRequestBytes bounds one JSON request body. Nothing a tool sends is close to
// this today (a path and a caption are the largest fields), so the cap only
// exists to stop a wedged or hostile local caller from streaming forever.
const maxRequestBytes = 8 << 20

// shutdownTimeout bounds the graceful drain performed by Close.
const shutdownTimeout = 5 * time.Second

// ErrNoAnswer reports that ask_options expired without the owner choosing. It
// is returned by OptionAsker implementations; the server turns it into the
// "sin respuesta" result instead of an error.
var ErrNoAnswer = errors.New("tools: no answer from the owner")

var (
	// errEscapes marks a path that is not inside the vault root.
	errEscapes = errors.New("path escapes the vault")
	// errMissing marks a path that does not exist inside the vault.
	errMissing = errors.New("file not found in the vault")
	// errNotFile marks an existing path that is a directory where a file is
	// required: a model mistake, not a server failure.
	errNotFile = errors.New("path is a directory, not a file")
)

// FileSender delivers a local vault file to the owner's chat. Implementations
// choose between sendDocument and sendPhoto (for example by extension) and know
// the owner's chat, so the model never chooses a recipient.
type FileSender interface {
	SendFile(ctx context.Context, path, caption string) error
}

// OptionAsker shows selectable buttons to the owner's chat and blocks until one
// is pressed. It must return ErrNoAnswer when the owner does not answer and
// honour the context it is given.
type OptionAsker interface {
	AskOptions(ctx context.Context, question string, options []string) (string, error)
}

// Config is the explicit configuration of the tools server.
type Config struct {
	// Vault is the store whose root contains every path a tool may touch. Pi
	// writes the vault with its own `write`/`edit`, so nothing here writes
	// through the store any more; it is still wired because `send_file` must
	// resolve the path it is given against the vault root and reject escapes.
	Vault *vault.Store
	// Files delivers vault files to the owner.
	Files FileSender
	// Asker shows option buttons and waits for the press.
	Asker OptionAsker
	// Logger receives one line per rejected request. Nil discards logging.
	Logger *log.Logger
	// AskTimeout bounds one ask_options wait. Non-positive: DefaultAskTimeout.
	AskTimeout time.Duration
}

// Server is the loopback HTTP endpoint backing the Pi tools.
type Server struct {
	cfg     Config
	token   string
	mux     *http.ServeMux
	handler http.Handler
	http    *http.Server

	mu sync.Mutex
	ln net.Listener
}

// New builds a Server from an explicit Config and generates the random token
// that every request must present. Nothing is bound until Start is called.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Vault == nil:
		return nil, errors.New("tools: Config.Vault is required")
	case cfg.Files == nil:
		return nil, errors.New("tools: Config.Files is required")
	case cfg.Asker == nil:
		return nil, errors.New("tools: Config.Asker is required")
	}
	if cfg.AskTimeout <= 0 {
		cfg.AskTimeout = DefaultAskTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}

	s := &Server{cfg: cfg, token: token}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("POST /tools/send_file", s.handleSendFile)
	s.mux.HandleFunc("POST /tools/ask_options", s.handleAskOptions)
	// The token is checked before routing, so an unauthenticated request can
	// never reach a handler and therefore never has a side effect.
	s.handler = s.authorize(s.mux)
	s.http = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// Token returns the per-process secret the extension sends as X-Alter-Token.
func (s *Server) Token() string { return s.token }

// Handler exposes the authenticated handler, which lets tests drive the real
// routes with httptest.
func (s *Server) Handler() http.Handler { return s.handler }

// Start binds an ephemeral port on the loopback interface and serves in the
// background. It returns the base URL to hand to Pi as ALTER_TOOLS_URL.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("tools: listen on loopback: %w", err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.cfg.Logger.Printf("tools: server stopped: %v", err)
		}
	}()
	return "http://" + ln.Addr().String(), nil
}

// Close drains in-flight requests and stops the server. It is safe to call on a
// server that never started.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.http.Shutdown(ctx); err != nil {
		return s.http.Close()
	}
	return nil
}

// newToken builds a 256-bit random token, hex encoded.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tools: generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// authorize rejects any request without the process token. The comparison is
// constant time so a local attacker cannot probe the token byte by byte.
func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(tokenHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			s.cfg.Logger.Printf("tools: rejected %s %s: invalid token", r.Method, r.URL.Path)
			writeError(w, http.StatusForbidden, "invalid or missing %s token", tokenHeader)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, errorBody{Error: fmt.Sprintf(format, args...)})
}

// decode reads the JSON body, answering 400 itself when it is unusable.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: %v", err)
		return false
	}
	return true
}

func (s *Server) handleSendFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Caption string `json:"caption"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	full, err := s.resolvePath(req.Path)
	if err != nil {
		writePathError(w, err)
		return
	}
	if err := s.cfg.Files.SendFile(r.Context(), full, req.Caption); err != nil {
		writeError(w, http.StatusBadGateway, "send file: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Path    string `json:"path"`
		Caption string `json:"caption,omitempty"`
		Sent    bool   `json:"sent"`
	}{Path: s.relative(full), Caption: req.Caption, Sent: true})
}

func (s *Server) handleAskOptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if !decode(w, r, &req) {
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}
	if len(req.Options) < 2 || len(req.Options) > 4 {
		writeError(w, http.StatusBadRequest, "ask_options needs between 2 and 4 options, got %d", len(req.Options))
		return
	}
	for i, opt := range req.Options {
		if strings.TrimSpace(opt) == "" {
			writeError(w, http.StatusBadRequest, "option %d is empty", i+1)
			return
		}
	}

	// The wait is bounded here as well as in the asker, so a wedged send can
	// never hold the turn open past the deadline.
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AskTimeout)
	defer cancel()

	answer, err := s.cfg.Asker.AskOptions(ctx, question, req.Options)
	switch {
	case errors.Is(err, ErrNoAnswer), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		s.cfg.Logger.Printf("tools: ask_options %q: %v", question, err)
		writeJSON(w, http.StatusOK, struct {
			Answer   string `json:"answer"`
			TimedOut bool   `json:"timed_out"`
		}{Answer: noAnswer, TimedOut: true})
	case err != nil:
		writeError(w, http.StatusBadGateway, "ask options: %v", err)
	default:
		writeJSON(w, http.StatusOK, struct {
			Answer   string `json:"answer"`
			TimedOut bool   `json:"timed_out"`
		}{Answer: answer})
	}
}

// writePathError maps a path resolution failure to the status the model sees.
func writePathError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errEscapes):
		writeError(w, http.StatusBadRequest, "%v", err)
	case errors.Is(err, errNotFile):
		writeError(w, http.StatusBadRequest, "%v", err)
	case errors.Is(err, errMissing):
		writeError(w, http.StatusNotFound, "%v", err)
	default:
		writeError(w, http.StatusInternalServerError, "%v", err)
	}
}

// relative turns a path inside the vault into the vault-relative form the model
// sees, so host paths never leak into the conversation.
func (s *Server) relative(path string) string {
	rel, err := filepath.Rel(s.cfg.Vault.Root(), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

// resolvePath turns a vault-relative path into an absolute one, refusing
// anything that leaves the root. Containment is checked against the
// symlink-resolved form of the path: a link inside the vault that points
// outside is an escape like any other. The path must name an existing
// non-directory file, because the only caller is send_file and there is nothing
// left to send for a path that does not exist.
func (s *Server) resolvePath(rel string) (string, error) {
	// A leading separator is read as "vault-relative", matching how models write
	// paths; the containment check below still rejects a real escape.
	clean := filepath.Clean(strings.TrimPrefix(rel, string(filepath.Separator)))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("%w: %q", errEscapes, rel)
	}

	root, err := filepath.Abs(s.cfg.Vault.Root())
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(root, clean))
	if err != nil {
		return "", err
	}
	if !within(root, full) {
		return "", fmt.Errorf("%w: %q", errEscapes, rel)
	}

	// Resolve both sides the same way: the root and the target through their
	// nearest existing ancestors. A path that cannot be resolved is a path that
	// cannot be proven contained, so it is rejected rather than accepted.
	realRoot, err := resolvedForm(root)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %v", errEscapes, rel, err)
	}
	resolved, err := resolvedForm(full)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %v", errEscapes, rel, err)
	}
	if !within(realRoot, resolved) {
		return "", fmt.Errorf("%w: %q", errEscapes, rel)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		return "", fmt.Errorf("%w: %q", errMissing, rel)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%w: %q", errNotFile, rel)
	}
	return resolved, nil
}

// resolvedForm returns the symlink-resolved form of path. When parts of it do not
// exist yet, the nearest existing ancestor is resolved and the missing
// components are appended, so a missing leaf still carries the resolution of its
// parent instead of falling back to the unresolved path.
func resolvedForm(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	dir, base := filepath.Split(filepath.Clean(path))
	if base == "" || dir == "" || filepath.Clean(dir) == filepath.Clean(path) {
		return "", fmt.Errorf("no existing ancestor of %q", path)
	}
	parent, err := resolvedForm(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, base), nil
}

// within reports whether path is base or lives under it.
func within(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
