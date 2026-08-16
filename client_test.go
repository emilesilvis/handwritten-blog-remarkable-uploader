package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLinkPollsUntilApprovedAndStoresTheIssuedToken(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/remarkable/device_authorization":
			writeJSON(response, http.StatusCreated, map[string]any{
				"device_code":      "opaque-device-code",
				"user_code":        "ABCD-EFGH",
				"verification_uri": serverURL(request) + "/remarkable/link?code=ABCD-EFGH",
				"expires_in":       600,
				"interval":         1,
			})
		case "/api/remarkable/device_token":
			polls++
			if polls == 1 {
				writeJSON(response, http.StatusPreconditionRequired, map[string]any{
					"error": "authorization_pending", "message": "Approve it",
				})
				return
			}
			writeJSON(response, http.StatusOK, map[string]any{
				"access_token": "hwb_rm_issued", "device_id": "device-id",
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	output := &bytes.Buffer{}
	configPath := filepath.Join(t.TempDir(), "config.json")
	app := &application{
		api:        newAPIClient(server.URL),
		configPath: configPath,
		stdout:     output,
		stderr:     io.Discard,
		sleep:      func(context.Context, time.Duration) error { return nil },
	}
	if err := app.link(context.Background()); err != nil {
		t.Fatal(err)
	}
	if polls != 2 {
		t.Fatalf("polls = %d, want 2", polls)
	}
	if !strings.Contains(output.String(), "ABCD-EFGH") {
		t.Fatalf("link output omitted user code: %s", output.String())
	}
	configuration, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.AccessToken != "hwb_rm_issued" || configuration.DeviceID != "device-id" {
		t.Fatalf("saved config = %#v", configuration)
	}
}

func TestUploadStreamsOneArchiveWithScopedAuthenticationAndIdempotency(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "notebook.rmdoc")
	if err := os.WriteFile(archivePath, []byte("native archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &notebookSnapshot{
		documentID: testDocumentID,
		title:      "Morning pages", archivePath: archivePath, contentHash: hash,
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer hwb_rm_secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if got, want := request.Header.Get("Idempotency-Key"), idempotencyKey(testDocumentID, hash); got != want {
			t.Errorf("idempotency key = %q, want %q", got, want)
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if request.FormValue("document_id") != testDocumentID || request.FormValue("content_hash") != hash {
			t.Errorf("multipart metadata = %#v", request.Form)
		}
		file, _, err := request.FormFile("archive")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		contents, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != "native archive" {
			t.Errorf("archive = %q", contents)
		}
		writeJSON(response, http.StatusAccepted, map[string]any{
			"document_id": testDocumentID, "title": "Morning pages", "state": "queued", "result": "accepted",
		})
	}))
	defer server.Close()

	result, err := newAPIClient(server.URL).upload(context.Background(), deviceConfig{AccessToken: "hwb_rm_secret"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Result != "accepted" || result.State != "queued" {
		t.Fatalf("upload result = %#v", result)
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func serverURL(request *http.Request) string {
	return "http://" + request.Host
}
