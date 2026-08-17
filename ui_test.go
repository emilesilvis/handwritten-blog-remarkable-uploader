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
	"testing"
)

func TestUIPreflightChecksLinkAndNotebookWithoutRestartingUI(t *testing.T) {
	rootPath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	configPath := linkedTestConfig(t)
	service := &fakeService{isActive: true}
	output := &bytes.Buffer{}
	app := &application{
		configPath: configPath,
		rootPath:   rootPath,
		cachePath:  t.TempDir(),
		service:    service,
		stdout:     output,
		stderr:     io.Discard,
	}

	if err := app.run(context.Background(), []string{"ui-preflight", testDocumentID}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "ready\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if !service.isActive {
		t.Fatal("preflight unexpectedly stopped Xochitl")
	}
}

func TestUIPreflightRejectsAnUnlinkedTablet(t *testing.T) {
	rootPath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	app := &application{
		configPath: filepath.Join(t.TempDir(), "missing.json"),
		rootPath:   rootPath,
		cachePath:  t.TempDir(),
		stdout:     io.Discard,
		stderr:     io.Discard,
	}

	err := app.run(context.Background(), []string{"ui-preflight", testDocumentID})
	if err == nil {
		t.Fatal("preflight accepted an unlinked tablet")
	}
}

func TestUISendRecordsSuccessForTheSelectedNotebook(t *testing.T) {
	rootPath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	var uploadedDocumentID string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		uploadedDocumentID = request.FormValue("document_id")
		writeJSON(response, http.StatusAccepted, map[string]any{
			"document_id": testDocumentID,
			"title":       "Morning pages",
			"state":       "queued",
			"result":      "accepted",
		})
	}))
	defer server.Close()

	cachePath := t.TempDir()
	app := &application{
		api:        newAPIClient(server.URL),
		configPath: linkedTestConfig(t),
		rootPath:   rootPath,
		cachePath:  cachePath,
		service:    &fakeService{isActive: true},
		stdout:     io.Discard,
		stderr:     io.Discard,
	}

	if err := app.run(context.Background(), []string{"ui-send", testDocumentID}); err != nil {
		t.Fatal(err)
	}
	if uploadedDocumentID != testDocumentID {
		t.Fatalf("uploaded document = %q, want %q", uploadedDocumentID, testDocumentID)
	}
	status, err := app.readUIStatus(testDocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "succeeded" || status.Message == "" {
		t.Fatalf("status = %#v", status)
	}
}

func TestUISendRecordsASafeFailureMessage(t *testing.T) {
	rootPath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, http.StatusServiceUnavailable, map[string]any{"error": "temporary"})
	}))
	defer server.Close()

	app := &application{
		api:        newAPIClient(server.URL),
		configPath: linkedTestConfig(t),
		rootPath:   rootPath,
		cachePath:  t.TempDir(),
		service:    &fakeService{isActive: true},
		stdout:     io.Discard,
		stderr:     io.Discard,
	}

	if err := app.run(context.Background(), []string{"ui-send", testDocumentID}); err == nil {
		t.Fatal("UI send unexpectedly succeeded")
	}
	status, err := app.readUIStatus(testDocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "failed" || status.Message != "Upload failed. Check the connection and try again." {
		t.Fatalf("status = %#v", status)
	}
}

func TestUIResultAndAckConsumeACompletedStatus(t *testing.T) {
	output := &bytes.Buffer{}
	app := &application{cachePath: t.TempDir(), stdout: output, stderr: io.Discard}
	if err := app.writeUIStatus(uiSendStatus{
		DocumentID: testDocumentID,
		State:      "succeeded",
		Message:    "done",
	}); err != nil {
		t.Fatal(err)
	}

	if err := app.run(context.Background(), []string{"ui-result", testDocumentID}); err != nil {
		t.Fatal(err)
	}
	var status uiSendStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "succeeded" {
		t.Fatalf("status = %#v", status)
	}
	if err := app.run(context.Background(), []string{"ui-ack", testDocumentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(app.uiStatusPath(testDocumentID)); !os.IsNotExist(err) {
		t.Fatalf("status still exists: %v", err)
	}
}

func TestUIResultIsIdleBeforeAnySend(t *testing.T) {
	output := &bytes.Buffer{}
	app := &application{cachePath: t.TempDir(), stdout: output, stderr: io.Discard}
	if err := app.run(context.Background(), []string{"ui-result", testDocumentID}); err != nil {
		t.Fatal(err)
	}
	var status uiSendStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "idle" || status.DocumentID != testDocumentID {
		t.Fatalf("status = %#v", status)
	}
}

func linkedTestConfig(t *testing.T) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(configPath, deviceConfig{
		AccessToken: "hwb_rm_test",
		DeviceID:    "device-id",
	}); err != nil {
		t.Fatal(err)
	}
	return configPath
}
