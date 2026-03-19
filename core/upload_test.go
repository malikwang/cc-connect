package core

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUploadServer_TokenGeneration(t *testing.T) {
	us := NewUploadServer(0, "http://localhost:9830", 0)
	url := us.CreateUploadLink("feishu:123:456", "test.pdf", "/tmp/workdir")
	if !strings.HasPrefix(url, "http://localhost:9830/upload/") {
		t.Fatalf("unexpected URL prefix: %s", url)
	}
	token := strings.TrimPrefix(url, "http://localhost:9830/upload/")
	if len(token) != 64 { // 32 bytes hex
		t.Fatalf("expected 64 char hex token, got %d chars: %s", len(token), token)
	}
}

func TestUploadServer_TokenExpiry(t *testing.T) {
	us := NewUploadServer(0, "http://localhost:9830", 0)
	us.CreateUploadLink("feishu:123:456", "test.pdf", "/tmp/workdir")

	// Manually expire the token
	us.mu.Lock()
	for _, tok := range us.tokens {
		tok.CreatedAt = time.Now().Add(-31 * time.Minute)
	}
	us.mu.Unlock()

	// Try to claim the expired token
	for tokenStr := range us.tokens {
		claimed := us.claimToken(tokenStr)
		if claimed != nil {
			t.Fatal("expired token should not be claimable")
		}
	}
}

func TestUploadServer_TokenSingleUse(t *testing.T) {
	us := NewUploadServer(0, "http://localhost:9830", 0)
	url := us.CreateUploadLink("feishu:123:456", "test.pdf", "/tmp/workdir")
	token := strings.TrimPrefix(url, "http://localhost:9830/upload/")

	// First claim should succeed
	claimed := us.claimToken(token)
	if claimed == nil {
		t.Fatal("first claim should succeed")
	}
	if claimed.SessionKey != "feishu:123:456" {
		t.Fatalf("unexpected session key: %s", claimed.SessionKey)
	}

	// Second claim should fail (token already used)
	claimed2 := us.claimToken(token)
	if claimed2 != nil {
		t.Fatal("second claim should fail (token already used)")
	}
}

func TestUploadServer_PeekDoesNotConsume(t *testing.T) {
	us := NewUploadServer(0, "http://localhost:9830", 0)
	url := us.CreateUploadLink("feishu:123:456", "test.pdf", "/tmp/workdir")
	token := strings.TrimPrefix(url, "http://localhost:9830/upload/")

	// Peek should not consume the token
	peeked := us.peekToken(token)
	if peeked == nil {
		t.Fatal("peek should return token")
	}

	// Claim should still work after peek
	claimed := us.claimToken(token)
	if claimed == nil {
		t.Fatal("claim should work after peek")
	}
}

func TestUploadServer_CleanExpiredTokens(t *testing.T) {
	us := NewUploadServer(0, "http://localhost:9830", 0)
	us.CreateUploadLink("feishu:123:456", "expired.pdf", "/tmp/workdir")
	us.CreateUploadLink("feishu:123:789", "valid.pdf", "/tmp/workdir")

	// Expire the first token
	us.mu.Lock()
	count := 0
	for _, tok := range us.tokens {
		if count == 0 {
			tok.CreatedAt = time.Now().Add(-31 * time.Minute)
		}
		count++
	}
	us.mu.Unlock()

	us.CleanExpiredTokens()

	us.mu.Lock()
	remaining := len(us.tokens)
	us.mu.Unlock()

	if remaining != 1 {
		t.Fatalf("expected 1 remaining token, got %d", remaining)
	}
}

func TestUploadServer_HandleUploadPage(t *testing.T) {
	us := NewUploadServer(0, "http://localhost:9830", 0)
	url := us.CreateUploadLink("feishu:123:456", "report.pdf", "/tmp/workdir")
	token := strings.TrimPrefix(url, "http://localhost:9830/upload/")

	req := httptest.NewRequest(http.MethodGet, "/upload/"+token, nil)
	w := httptest.NewRecorder()
	us.handleUpload(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "report.pdf") {
		t.Fatal("upload page should contain the file name")
	}
}

func TestUploadServer_HandleUploadPage_InvalidToken(t *testing.T) {
	us := NewUploadServer(0, "http://localhost:9830", 0)

	req := httptest.NewRequest(http.MethodGet, "/upload/invalidtoken123", nil)
	w := httptest.NewRecorder()
	us.handleUpload(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestUploadServer_HandleUploadFile(t *testing.T) {
	tmpDir := t.TempDir()
	us := NewUploadServer(0, "http://localhost:9830", 500)
	url := us.CreateUploadLink("feishu:123:456", "test.txt", tmpDir)
	token := strings.TrimPrefix(url, "http://localhost:9830/upload/")

	// Create multipart form
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("hello world"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload/"+token, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	us.handleUpload(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	// Verify file was saved
	savedPath := filepath.Join(tmpDir, ".cc-connect", "attachments", "test.txt")
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("file not saved: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("unexpected file content: %s", string(data))
	}
}

func TestUploadServer_HandleUploadFile_TokenReuse(t *testing.T) {
	tmpDir := t.TempDir()
	us := NewUploadServer(0, "http://localhost:9830", 500)
	url := us.CreateUploadLink("feishu:123:456", "test.txt", tmpDir)
	token := strings.TrimPrefix(url, "http://localhost:9830/upload/")

	// First upload
	makeUploadReq := func() *http.Request {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "test.txt")
		fw.Write([]byte("hello"))
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/upload/"+token, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return req
	}

	w1 := httptest.NewRecorder()
	us.handleUpload(w1, makeUploadReq())
	if w1.Result().StatusCode != http.StatusOK {
		t.Fatal("first upload should succeed")
	}

	// Second upload with same token should fail
	w2 := httptest.NewRecorder()
	us.handleUpload(w2, makeUploadReq())
	if w2.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("second upload should fail, got %d", w2.Result().StatusCode)
	}
}

func TestUploadServer_EscapeHTML(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"hello", "hello"},
		{"<script>", "&lt;script&gt;"},
		{"a&b", "a&amp;b"},
		{`"quoted"`, "&quot;quoted&quot;"},
	}
	for _, tc := range tests {
		got := escapeHTMLUpload(tc.input)
		if got != tc.expected {
			t.Errorf("escapeHTMLUpload(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestUploadServer_ExternalURL(t *testing.T) {
	us := NewUploadServer(9830, "https://my.server.com:9830/", 0)
	url := us.CreateUploadLink("feishu:123:456", "test.pdf", "/tmp")
	if !strings.HasPrefix(url, "https://my.server.com:9830/upload/") {
		t.Fatalf("unexpected URL: %s", url)
	}
}

func TestUploadServer_DefaultExternalURL(t *testing.T) {
	us := NewUploadServer(9999, "", 0)
	url := us.CreateUploadLink("feishu:123:456", "test.pdf", "/tmp")
	expected := fmt.Sprintf("http://localhost:%d/upload/", 9999)
	if !strings.HasPrefix(url, expected) {
		t.Fatalf("expected prefix %s, got %s", expected, url)
	}
}
