package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	maxSnapshotEntries = 1000
	maxSnapshotBytes   = int64(200 << 20)
	maxSnapshotFile    = int64(100 << 20)
	maxMetadataBytes   = int64(1 << 20)
)

var documentIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type serviceController interface {
	active(context.Context) (bool, error)
	stop(context.Context) error
	start(context.Context) error
}

type systemdService struct {
	name string
}

func (service systemdService) active(ctx context.Context) (bool, error) {
	output, err := exec.CommandContext(ctx, "systemctl", "is-active", service.name).CombinedOutput()
	state := strings.TrimSpace(string(output))
	switch state {
	case "active", "activating", "reloading", "deactivating":
		return true, nil
	case "inactive", "failed", "unknown":
		var exit *exec.ExitError
		if err == nil || errors.As(err, &exit) {
			return false, nil
		}
	}
	if err != nil {
		return false, fmt.Errorf("check %s service: %w: %s", service.name, err, state)
	}
	return false, fmt.Errorf("check %s service: unexpected state %q", service.name, state)
}

func (service systemdService) stop(ctx context.Context) error {
	return service.run(ctx, "stop")
}

func (service systemdService) start(ctx context.Context) error {
	return service.run(ctx, "start")
}

func (service systemdService) run(ctx context.Context, action string) error {
	output, err := exec.CommandContext(ctx, "systemctl", action, service.name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", action, service.name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

type notebookSnapshotter struct {
	rootPath  string
	cachePath string
	service   serviceController
}

type notebookSnapshot struct {
	documentID  string
	title       string
	archivePath string
	contentHash string
	cleanup     func()
}

type notebookInfo struct {
	documentID string
	title      string
}

func (snapshotter notebookSnapshotter) create(ctx context.Context, documentID string) (*notebookSnapshot, error) {
	if !validDocumentID(documentID) {
		return nil, errors.New("document UUID must use the canonical 8-4-4-4-12 form")
	}
	if err := os.MkdirAll(snapshotter.cachePath, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(snapshotter.cachePath, 0o700); err != nil {
		return nil, err
	}

	lock, err := os.OpenFile(filepath.Join(snapshotter.cachePath, "sync.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.New("another notebook sync is already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	stagingPath, err := os.MkdirTemp(snapshotter.cachePath, "snapshot-*")
	if err != nil {
		return nil, err
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			os.RemoveAll(stagingPath)
		}
	}()

	if err := snapshotter.copyWithXochitlStopped(ctx, documentID, stagingPath); err != nil {
		return nil, err
	}
	title, err := readNotebookTitle(filepath.Join(stagingPath, documentID+".metadata"), documentID)
	if err != nil {
		return nil, err
	}
	archivePath, contentHash, err := createDeterministicArchive(snapshotter.cachePath, stagingPath)
	if err != nil {
		return nil, err
	}
	os.RemoveAll(stagingPath)
	cleanupStaging = false

	return &notebookSnapshot{
		documentID:  documentID,
		title:       title,
		archivePath: archivePath,
		contentHash: contentHash,
		cleanup: func() {
			os.Remove(archivePath)
		},
	}, nil
}

func (snapshotter notebookSnapshotter) copyWithXochitlStopped(ctx context.Context, documentID, stagingPath string) error {
	return snapshotter.withXochitlStopped(ctx, func() error {
		return snapshotter.copyDocument(ctx, documentID, stagingPath)
	})
}

func (snapshotter notebookSnapshotter) withXochitlStopped(ctx context.Context, operation func() error) error {
	wasActive, err := snapshotter.service.active(ctx)
	if err != nil {
		return err
	}
	restart := func() error {
		if !wasActive {
			return nil
		}
		restartContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := snapshotter.service.start(restartContext); err != nil {
			return fmt.Errorf("restart tablet UI: %w", err)
		}
		return nil
	}
	if wasActive {
		if err := ctx.Err(); err != nil {
			return err
		}
		stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		stopErr := snapshotter.service.stop(stopContext)
		cancel()
		if stopErr != nil {
			return errors.Join(fmt.Errorf("stop tablet UI: %w", stopErr), restart())
		}
	}

	operationErr := operation()
	return errors.Join(operationErr, restart())
}

func (snapshotter notebookSnapshotter) list(ctx context.Context) ([]notebookInfo, error) {
	if err := os.MkdirAll(snapshotter.cachePath, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(snapshotter.cachePath, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(snapshotter.cachePath, "sync.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.New("another notebook operation is already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	var notebooks []notebookInfo
	err = snapshotter.withXochitlStopped(ctx, func() error {
		matches, err := filepath.Glob(filepath.Join(snapshotter.rootPath, "*.metadata"))
		if err != nil {
			return err
		}
		if len(matches) > maxSnapshotEntries {
			return errors.New("the local library has too many entries to list safely")
		}
		for _, metadataPath := range matches {
			if err := ctx.Err(); err != nil {
				return err
			}
			documentID := strings.TrimSuffix(filepath.Base(metadataPath), ".metadata")
			if !validDocumentID(documentID) {
				continue
			}
			info, err := os.Lstat(metadataPath)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > maxMetadataBytes {
				continue
			}
			contents, err := os.ReadFile(metadataPath)
			if err != nil {
				return err
			}
			var metadata struct {
				VisibleName string `json:"visibleName"`
				Type        string `json:"type"`
				Deleted     bool   `json:"deleted"`
			}
			if json.Unmarshal(contents, &metadata) != nil || metadata.Type != "DocumentType" || metadata.Deleted {
				continue
			}
			title := strings.TrimSpace(metadata.VisibleName)
			if title == "" {
				title = documentID
			}
			notebooks = append(notebooks, notebookInfo{documentID: documentID, title: title})
		}
		return nil
	})
	sort.Slice(notebooks, func(first, second int) bool {
		return strings.ToLower(notebooks[first].title) < strings.ToLower(notebooks[second].title)
	})
	return notebooks, err
}

func (snapshotter notebookSnapshotter) copyDocument(ctx context.Context, documentID, stagingPath string) error {
	required := []string{documentID + ".metadata", documentID + ".content"}
	optional := []string{documentID + ".pagedata", documentID + ".pdf", documentID + ".epub"}
	entryCount := 0
	totalBytes := int64(0)

	for _, name := range append(required, optional...) {
		sourcePath := filepath.Join(snapshotter.rootPath, name)
		info, err := os.Lstat(sourcePath)
		if errors.Is(err, os.ErrNotExist) && contains(required, name) {
			return fmt.Errorf("selected notebook is missing %s", name)
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".metadata") && info.Size() > maxMetadataBytes {
			return errors.New("selected notebook metadata exceeds the processing limit")
		}
		if err := copyRegularFile(ctx, sourcePath, filepath.Join(stagingPath, name), info, &entryCount, &totalBytes); err != nil {
			return err
		}
	}

	pageRoot := filepath.Join(snapshotter.rootPath, documentID)
	rootInfo, err := os.Lstat(pageRoot)
	if err != nil {
		return fmt.Errorf("read selected notebook pages: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("selected notebook page data is not a regular directory")
	}
	pageEntryCount := entryCount
	err = filepath.WalkDir(pageRoot, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(snapshotter.rootPath, sourcePath)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(stagingPath, relative), 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyRegularFile(ctx, sourcePath, filepath.Join(stagingPath, relative), info, &entryCount, &totalBytes)
	})
	if err != nil {
		return err
	}
	if entryCount == pageEntryCount {
		return errors.New("selected notebook contains no native page data")
	}
	return nil
}

func copyRegularFile(ctx context.Context, sourcePath, destinationPath string, info fs.FileInfo,
	entryCount *int, totalBytes *int64) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing non-regular notebook file %s", sourcePath)
	}
	if info.Size() > maxSnapshotFile {
		return fmt.Errorf("notebook file %s exceeds the per-file limit", sourcePath)
	}
	*entryCount += 1
	*totalBytes += info.Size()
	if *entryCount > maxSnapshotEntries || *totalBytes > maxSnapshotBytes {
		return errors.New("selected notebook exceeds snapshot limits")
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, &contextReader{ctx: ctx, reader: source})
	closeErr := destination.Close()
	return errors.Join(copyErr, closeErr)
}

func createDeterministicArchive(cachePath, stagingPath string) (string, string, error) {
	archive, err := os.CreateTemp(cachePath, "notebook-*.rmdoc")
	if err != nil {
		return "", "", err
	}
	archivePath := archive.Name()
	cleanup := true
	defer func() {
		archive.Close()
		if cleanup {
			os.Remove(archivePath)
		}
	}()
	if err := archive.Chmod(0o600); err != nil {
		return "", "", err
	}

	var files []string
	err = filepath.WalkDir(stagingPath, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			files = append(files, filePath)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	sort.Strings(files)
	zipWriter := zip.NewWriter(archive)
	for _, filePath := range files {
		relative, err := filepath.Rel(stagingPath, filePath)
		if err != nil {
			return "", "", err
		}
		header := &zip.FileHeader{
			Name:     filepath.ToSlash(relative),
			Method:   zip.Deflate,
			Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		header.SetMode(0o600)
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return "", "", err
		}
		file, err := os.Open(filePath)
		if err != nil {
			return "", "", err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", "", err
		}
	}
	if err := zipWriter.Close(); err != nil {
		return "", "", err
	}
	if err := archive.Sync(); err != nil {
		return "", "", err
	}
	if err := archive.Close(); err != nil {
		return "", "", err
	}
	hash, err := fileSHA256(archivePath)
	if err != nil {
		return "", "", err
	}
	cleanup = false
	return archivePath, hash, nil
}

func readNotebookTitle(metadataPath, fallback string) (string, error) {
	contents, err := os.ReadFile(metadataPath)
	if err != nil {
		return "", err
	}
	var metadata struct {
		VisibleName string `json:"visibleName"`
	}
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return "", fmt.Errorf("decode notebook metadata: %w", err)
	}
	title := strings.TrimSpace(metadata.VisibleName)
	if title == "" {
		title = fallback
	}
	runes := []rune(title)
	if len(runes) > 200 {
		title = string(runes[:200])
	}
	return title, nil
}

func fileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validDocumentID(value string) bool {
	return documentIDPattern.MatchString(value)
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
