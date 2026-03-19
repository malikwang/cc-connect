package core

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed upload_page.html
var uploadPageHTML string

const (
	uploadTokenBytes  = 32
	uploadTokenExpiry = 30 * time.Minute
	uploadMemoryLimit = 32 << 20 // 32 MB memory for multipart parsing
)

// uploadToken represents a single-use, time-limited upload token.
type uploadToken struct {
	SessionKey string
	FileName   string
	WorkDir    string
	CreatedAt  time.Time
	Used       bool
}

// UploadServer provides a local HTTP server for browser-based file uploads.
// It is used as a fallback when platform file download APIs fail (e.g., files >100MB).
type UploadServer struct {
	port        int
	externalURL string
	maxSizeMB   int
	server      *http.Server
	engines     map[string]*Engine
	tokens      map[string]*uploadToken
	mu          sync.Mutex
}

// NewUploadServer creates a new upload server.
func NewUploadServer(port int, externalURL string, maxSizeMB int) *UploadServer {
	if port <= 0 {
		port = 9830
	}
	if maxSizeMB <= 0 {
		maxSizeMB = 500
	}
	if externalURL == "" {
		if ip := detectLocalIP(); ip != "" {
			externalURL = fmt.Sprintf("http://%s:%d", ip, port)
		} else {
			externalURL = fmt.Sprintf("http://localhost:%d", port)
		}
	}
	externalURL = strings.TrimRight(externalURL, "/")
	return &UploadServer{
		port:        port,
		externalURL: externalURL,
		maxSizeMB:   maxSizeMB,
		engines:     make(map[string]*Engine),
		tokens:      make(map[string]*uploadToken),
	}
}

// RegisterEngine associates a project engine with this upload server.
func (us *UploadServer) RegisterEngine(name string, e *Engine) {
	us.mu.Lock()
	defer us.mu.Unlock()
	us.engines[name] = e
}

// Start launches the HTTP server in a background goroutine.
func (us *UploadServer) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload/", us.handleUpload)

	addr := fmt.Sprintf(":%d", us.port)
	us.server = &http.Server{Addr: addr, Handler: mux}

	go func() {
		slog.Info("upload: server started", "addr", addr)
		if err := us.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("upload: server error", "error", err)
		}
	}()
}

// Stop gracefully shuts down the upload server.
func (us *UploadServer) Stop() {
	if us.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		us.server.Shutdown(ctx)
	}
}

// CreateUploadLink generates a single-use token and returns the full upload URL.
func (us *UploadServer) CreateUploadLink(sessionKey, fileName, workDir string) string {
	token := us.generateToken()
	us.mu.Lock()
	us.tokens[token] = &uploadToken{
		SessionKey: sessionKey,
		FileName:   fileName,
		WorkDir:    workDir,
		CreatedAt:  time.Now(),
	}
	us.mu.Unlock()
	return fmt.Sprintf("%s/upload/%s", us.externalURL, token)
}

func (us *UploadServer) generateToken() string {
	b := make([]byte, uploadTokenBytes)
	if _, err := rand.Read(b); err != nil {
		panic("upload: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// claimToken retrieves and marks a token as used. Returns nil if token is invalid, expired, or already used.
func (us *UploadServer) claimToken(token string) *uploadToken {
	us.mu.Lock()
	defer us.mu.Unlock()
	ut, ok := us.tokens[token]
	if !ok {
		return nil
	}
	if ut.Used {
		return nil
	}
	if time.Since(ut.CreatedAt) > uploadTokenExpiry {
		delete(us.tokens, token)
		return nil
	}
	ut.Used = true
	return ut
}

// peekToken retrieves a token without marking it as used (for GET requests).
func (us *UploadServer) peekToken(token string) *uploadToken {
	us.mu.Lock()
	defer us.mu.Unlock()
	ut, ok := us.tokens[token]
	if !ok {
		return nil
	}
	if ut.Used {
		return nil
	}
	if time.Since(ut.CreatedAt) > uploadTokenExpiry {
		delete(us.tokens, token)
		return nil
	}
	return ut
}

func (us *UploadServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/upload/")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		us.handleUploadPage(w, r, token)
	case http.MethodPost:
		us.handleUploadFile(w, r, token)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (us *UploadServer) handleUploadPage(w http.ResponseWriter, _ *http.Request, token string) {
	ut := us.peekToken(token)
	if ut == nil {
		http.Error(w, "invalid or expired upload link", http.StatusNotFound)
		return
	}

	page := strings.ReplaceAll(uploadPageHTML, "{{.FileName}}", escapeHTMLUpload(ut.FileName))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(page))
}

func (us *UploadServer) handleUploadFile(w http.ResponseWriter, r *http.Request, token string) {
	ut := us.claimToken(token)
	if ut == nil {
		http.Error(w, "invalid, expired, or already used upload link", http.StatusNotFound)
		return
	}

	maxBytes := int64(us.maxSizeMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	if err := r.ParseMultipartForm(uploadMemoryLimit); err != nil {
		http.Error(w, "failed to parse upload: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file in request", http.StatusBadRequest)
		return
	}
	defer file.Close()

	attachDir := filepath.Join(ut.WorkDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Error("upload: create attach dir failed", "error", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	fileName := header.Filename
	if fileName == "" {
		fileName = ut.FileName
	}
	destPath := filepath.Join(attachDir, fileName)

	dst, err := os.Create(destPath)
	if err != nil {
		slog.Error("upload: create file failed", "error", err, "path", destPath)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	written, err := io.Copy(dst, file)
	dst.Close()
	if err != nil {
		os.Remove(destPath)
		slog.Error("upload: write file failed", "error", err)
		http.Error(w, "failed to save file", http.StatusInternalServerError)
		return
	}

	slog.Info("upload: file saved", "path", destPath, "size", written, "session", ut.SessionKey)

	// Notify agent and user
	us.mu.Lock()
	engines := make([]*Engine, 0, len(us.engines))
	for _, e := range us.engines {
		engines = append(engines, e)
	}
	us.mu.Unlock()

	for _, e := range engines {
		e.InjectUploadedFile(ut.SessionKey, destPath, fileName)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// escapeHTMLUpload performs minimal HTML escaping for template values.
func escapeHTMLUpload(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// CleanExpiredTokens removes expired tokens. Called periodically.
func (us *UploadServer) CleanExpiredTokens() {
	us.mu.Lock()
	defer us.mu.Unlock()
	now := time.Now()
	for k, t := range us.tokens {
		if now.Sub(t.CreatedAt) > uploadTokenExpiry || t.Used {
			delete(us.tokens, k)
		}
	}
}

// detectLocalIP returns the first non-loopback IPv4 address of the machine.
func detectLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return ""
}
