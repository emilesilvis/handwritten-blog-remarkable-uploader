package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestListJSONReturnsVersionedNotebookContract(t *testing.T) {
	rootPath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	service := &fakeService{isActive: true}
	output := &bytes.Buffer{}
	app := &application{
		rootPath:  rootPath,
		cachePath: t.TempDir(),
		service:   service,
		stdout:    output,
		stderr:    io.Discard,
	}

	if err := app.run(context.Background(), []string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}

	var got notebookListOutput
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode list output: %v\n%s", err, output.String())
	}
	if got.Version != 1 {
		t.Fatalf("contract version = %d, want 1", got.Version)
	}
	if len(got.Notebooks) != 1 {
		t.Fatalf("notebooks = %#v, want one", got.Notebooks)
	}
	if got.Notebooks[0].DocumentID != testDocumentID || got.Notebooks[0].Title != "Morning pages" {
		t.Fatalf("notebook = %#v", got.Notebooks[0])
	}
	if service.isActive != true {
		t.Fatal("Xochitl was not restarted after the JSON listing")
	}
}

func TestListJSONUsesAnEmptyArrayForAnEmptyLibrary(t *testing.T) {
	output := &bytes.Buffer{}
	app := &application{
		rootPath:  t.TempDir(),
		cachePath: t.TempDir(),
		service:   &fakeService{},
		stdout:    output,
		stderr:    io.Discard,
	}

	if err := app.run(context.Background(), []string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"version\":1,\"notebooks\":[]}\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestListRejectsUnknownOptions(t *testing.T) {
	app := &application{stdout: io.Discard, stderr: io.Discard}

	err := app.run(context.Background(), []string{"list", "--yaml"})

	if err == nil || err.Error() != "usage: handwritten-blog list [--json]" {
		t.Fatalf("error = %v", err)
	}
}

func TestSyncWithoutAUUIDPromptsForOneLocalNotebook(t *testing.T) {
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

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(configPath, deviceConfig{
		AccessToken: "hwb_rm_test",
		DeviceID:    "device-id",
	}); err != nil {
		t.Fatal(err)
	}

	output := &bytes.Buffer{}
	app := &application{
		api:        newAPIClient(server.URL),
		configPath: configPath,
		rootPath:   rootPath,
		cachePath:  t.TempDir(),
		service:    &fakeService{},
		stdin:      strings.NewReader("1\n"),
		stdout:     output,
		stderr:     io.Discard,
	}

	if err := app.run(context.Background(), []string{"sync"}); err != nil {
		t.Fatal(err)
	}

	if uploadedDocumentID != testDocumentID {
		t.Fatalf("uploaded document = %q, want %q", uploadedDocumentID, testDocumentID)
	}
	if !strings.Contains(output.String(), "1. Morning pages") {
		t.Fatalf("selection output omitted notebook: %s", output.String())
	}
	if !strings.Contains(output.String(), `Uploading "Morning pages"`) {
		t.Fatalf("sync output omitted upload: %s", output.String())
	}
}
