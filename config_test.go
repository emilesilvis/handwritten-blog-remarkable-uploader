package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigIsWrittenAtomicallyWithPrivatePermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "config.json")
	want := deviceConfig{AccessToken: "hwb_rm_secret", DeviceID: "device-id"}

	if err := saveConfig(configPath, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("config permissions = %o, want 600", permissions)
	}
	got, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("config = %#v, want %#v", got, want)
	}

	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(configPath); err == nil {
		t.Fatal("loadConfig accepted a credential readable by other users")
	}
}

func TestIdempotencyKeyIncludesDocumentAndRevision(t *testing.T) {
	first := idempotencyKey("document-a", "revision")
	second := idempotencyKey("document-b", "revision")
	if first == second {
		t.Fatal("idempotency keys collided across documents")
	}
	if first != idempotencyKey("document-a", "revision") {
		t.Fatal("idempotency key is not deterministic")
	}
}
