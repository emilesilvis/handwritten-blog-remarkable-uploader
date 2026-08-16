package main

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testDocumentID = "11111111-2222-3333-4444-555555555555"

type fakeService struct {
	isActive bool
	stopErr  error
	events   []string
}

type cancelDuringStopService struct {
	isActive bool
	cancel   context.CancelFunc
	events   []string
}

func (service *cancelDuringStopService) active(context.Context) (bool, error) {
	service.events = append(service.events, "active")
	return service.isActive, nil
}

func (service *cancelDuringStopService) stop(ctx context.Context) error {
	service.events = append(service.events, "stop")
	service.isActive = false
	service.cancel()
	return ctx.Err()
}

func (service *cancelDuringStopService) start(context.Context) error {
	service.events = append(service.events, "start")
	service.isActive = true
	return nil
}

func (service *fakeService) active(context.Context) (bool, error) {
	service.events = append(service.events, "active")
	return service.isActive, nil
}

func (service *fakeService) stop(context.Context) error {
	service.events = append(service.events, "stop")
	service.isActive = false
	return service.stopErr
}

func (service *fakeService) start(context.Context) error {
	service.events = append(service.events, "start")
	service.isActive = true
	return nil
}

func TestSnapshotStopsCopiesAndRestartsBeforeReturning(t *testing.T) {
	rootPath := t.TempDir()
	cachePath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	service := &fakeService{isActive: true}
	snapshotter := notebookSnapshotter{rootPath: rootPath, cachePath: cachePath, service: service}

	first, err := snapshotter.create(context.Background(), testDocumentID)
	if err != nil {
		t.Fatal(err)
	}
	defer first.cleanup()
	if !service.isActive {
		t.Fatal("Xochitl was not running when create returned")
	}
	if want := []string{"active", "stop", "start"}; !reflect.DeepEqual(service.events, want) {
		t.Fatalf("service events = %v, want %v", service.events, want)
	}
	if first.title != "Morning pages" {
		t.Fatalf("title = %q", first.title)
	}
	assertArchiveEntries(t, first.archivePath, []string{
		testDocumentID + ".content",
		testDocumentID + ".metadata",
		testDocumentID + "/page-1.rm",
	})

	service.events = nil
	second, err := snapshotter.create(context.Background(), testDocumentID)
	if err != nil {
		t.Fatal(err)
	}
	defer second.cleanup()
	if first.contentHash != second.contentHash {
		t.Fatalf("deterministic snapshot hashes differ: %s != %s", first.contentHash, second.contentHash)
	}
}

func TestSnapshotRestartsXochitlAfterARejectedFile(t *testing.T) {
	rootPath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	linkPath := filepath.Join(rootPath, testDocumentID, "unsafe.rm")
	if err := os.Symlink("page-1.rm", linkPath); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{isActive: true}
	snapshotter := notebookSnapshotter{rootPath: rootPath, cachePath: t.TempDir(), service: service}

	_, err := snapshotter.create(context.Background(), testDocumentID)

	if err == nil || !strings.Contains(err.Error(), "non-regular notebook file") {
		t.Fatalf("error = %v, want non-regular file rejection", err)
	}
	if !service.isActive {
		t.Fatal("Xochitl stayed stopped after snapshot failure")
	}
	if want := []string{"active", "stop", "start"}; !reflect.DeepEqual(service.events, want) {
		t.Fatalf("service events = %v, want %v", service.events, want)
	}
}

func TestSnapshotRestartsXochitlWhenInterruptedDuringStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &cancelDuringStopService{isActive: true, cancel: cancel}
	snapshotter := notebookSnapshotter{service: service}

	err := snapshotter.withXochitlStopped(ctx, func() error {
		return ctx.Err()
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if !service.isActive {
		t.Fatal("Xochitl stayed stopped after interruption during stop")
	}
	if want := []string{"active", "stop", "start"}; !reflect.DeepEqual(service.events, want) {
		t.Fatalf("service events = %v, want %v", service.events, want)
	}
}

func TestSnapshotRestartsXochitlWhenInterruptedDuringCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &fakeService{isActive: true}
	snapshotter := notebookSnapshotter{service: service}

	err := snapshotter.withXochitlStopped(ctx, func() error {
		cancel()
		return ctx.Err()
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if !service.isActive {
		t.Fatal("Xochitl stayed stopped after interruption during copy")
	}
	if want := []string{"active", "stop", "start"}; !reflect.DeepEqual(service.events, want) {
		t.Fatalf("service events = %v, want %v", service.events, want)
	}
}

func TestSnapshotRestartsXochitlAfterAmbiguousStopFailure(t *testing.T) {
	stopErr := errors.New("systemctl returned after stopping Xochitl")
	service := &fakeService{isActive: true, stopErr: stopErr}
	snapshotter := notebookSnapshotter{service: service}
	operationRan := false

	err := snapshotter.withXochitlStopped(context.Background(), func() error {
		operationRan = true
		return nil
	})

	if !errors.Is(err, stopErr) {
		t.Fatalf("error = %v, want stop error", err)
	}
	if operationRan {
		t.Fatal("snapshot operation ran after stop failure")
	}
	if !service.isActive {
		t.Fatal("Xochitl stayed stopped after ambiguous stop failure")
	}
	if want := []string{"active", "stop", "start"}; !reflect.DeepEqual(service.events, want) {
		t.Fatalf("service events = %v, want %v", service.events, want)
	}
}

func TestSnapshotDoesNotStartAnAlreadyStoppedXochitl(t *testing.T) {
	rootPath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	service := &fakeService{isActive: false}
	snapshotter := notebookSnapshotter{rootPath: rootPath, cachePath: t.TempDir(), service: service}

	snapshot, err := snapshotter.create(context.Background(), testDocumentID)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.cleanup()
	if want := []string{"active"}; !reflect.DeepEqual(service.events, want) {
		t.Fatalf("service events = %v, want %v", service.events, want)
	}
}

func TestListReadsMetadataOnlyWhileXochitlIsStopped(t *testing.T) {
	rootPath := t.TempDir()
	writeNotebook(t, rootPath, "native page")
	service := &fakeService{isActive: true}
	snapshotter := notebookSnapshotter{rootPath: rootPath, cachePath: t.TempDir(), service: service}

	notebooks, err := snapshotter.list(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	want := []notebookInfo{{documentID: testDocumentID, title: "Morning pages"}}
	if !reflect.DeepEqual(notebooks, want) {
		t.Fatalf("notebooks = %#v, want %#v", notebooks, want)
	}
	if wantEvents := []string{"active", "stop", "start"}; !reflect.DeepEqual(service.events, wantEvents) {
		t.Fatalf("service events = %v, want %v", service.events, wantEvents)
	}
}

func writeNotebook(t *testing.T, rootPath, page string) {
	t.Helper()
	pagePath := filepath.Join(rootPath, testDocumentID)
	if err := os.MkdirAll(pagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		testDocumentID + ".metadata":               `{"visibleName":"Morning pages","type":"DocumentType"}`,
		testDocumentID + ".content":                `{"fileType":"notebook","pages":["page-1"]}`,
		filepath.Join(testDocumentID, "page-1.rm"): page,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertArchiveEntries(t *testing.T, archivePath string, want []string) {
	t.Helper()
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	got := make([]string, 0, len(archive.File))
	for _, file := range archive.File {
		got = append(got, file.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("archive entries = %v, want %v", got, want)
	}
}
