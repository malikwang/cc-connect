package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenManager_SetAndGet(t *testing.T) {
	dir := t.TempDir()
	tm, err := NewUserTokenManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	if got := tm.GetToken("user1"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if tm.HasToken("user1") {
		t.Fatal("expected HasToken=false")
	}

	if err := tm.SetToken("user1", "sk-ant-xxx"); err != nil {
		t.Fatal(err)
	}
	if got := tm.GetToken("user1"); got != "sk-ant-xxx" {
		t.Fatalf("expected sk-ant-xxx, got %q", got)
	}
	if !tm.HasToken("user1") {
		t.Fatal("expected HasToken=true")
	}
}

func TestTokenManager_Clear(t *testing.T) {
	dir := t.TempDir()
	tm, err := NewUserTokenManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	tm.SetToken("user1", "sk-ant-xxx")
	if err := tm.ClearToken("user1"); err != nil {
		t.Fatal(err)
	}
	if tm.HasToken("user1") {
		t.Fatal("expected HasToken=false after clear")
	}
}

func TestTokenManager_Persistence(t *testing.T) {
	dir := t.TempDir()
	tm, err := NewUserTokenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	tm.SetToken("ou_abc", "sk-ant-123")

	// Reload from disk
	tm2, err := NewUserTokenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := tm2.GetToken("ou_abc"); got != "sk-ant-123" {
		t.Fatalf("expected sk-ant-123 after reload, got %q", got)
	}
}

func TestTokenManager_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	tm, err := NewUserTokenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	tm.SetToken("u1", "tok")

	info, err := os.Stat(filepath.Join(dir, "user_tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Fatalf("expected file perm 0600, got %04o", perm)
	}
}

func TestTokenManager_MultipleUsers(t *testing.T) {
	dir := t.TempDir()
	tm, err := NewUserTokenManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	tm.SetToken("u1", "tok1")
	tm.SetToken("u2", "tok2")

	if got := tm.GetToken("u1"); got != "tok1" {
		t.Fatalf("u1: expected tok1, got %q", got)
	}
	if got := tm.GetToken("u2"); got != "tok2" {
		t.Fatalf("u2: expected tok2, got %q", got)
	}

	tm.ClearToken("u1")
	if tm.HasToken("u1") {
		t.Fatal("u1 should be cleared")
	}
	if !tm.HasToken("u2") {
		t.Fatal("u2 should still exist")
	}
}
