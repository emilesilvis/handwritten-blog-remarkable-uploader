package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const uiStatusVersion = 1

type uiSendStatus struct {
	Version    int    `json:"version"`
	DocumentID string `json:"document_id"`
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

func (app *application) uiPreflight(documentID string, announce bool) error {
	if !validDocumentID(documentID) {
		return errors.New("document UUID must use the canonical 8-4-4-4-12 form")
	}
	if _, err := loadConfig(app.configPath); err != nil {
		return fmt.Errorf("load linked device: %w", err)
	}

	metadataPath := filepath.Join(app.rootPath, documentID+".metadata")
	info, err := os.Lstat(metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("the selected notebook is no longer available")
		}
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxMetadataBytes {
		return errors.New("the selected notebook metadata is not safe to read")
	}

	if err := app.ensureUICache(); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(app.cachePath, "sync.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another notebook sync is already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	if announce {
		fmt.Fprintln(app.stdout, "ready")
	}
	return nil
}

func (app *application) uiSend(ctx context.Context, documentID string) error {
	if err := app.uiPreflight(documentID, false); err != nil {
		_ = app.writeUIStatus(uiSendStatus{
			DocumentID: documentID,
			State:      "failed",
			Message:    "Could not start the upload. Link the tablet and try again.",
		})
		return err
	}
	if err := app.writeUIStatus(uiSendStatus{
		DocumentID: documentID,
		State:      "running",
		Message:    "Sending notebook to handwritten.blog…",
	}); err != nil {
		return fmt.Errorf("record UI upload state: %w", err)
	}

	if err := app.sync(ctx, documentID); err != nil {
		statusErr := app.writeUIStatus(uiSendStatus{
			DocumentID: documentID,
			State:      "failed",
			Message:    "Upload failed. Check the connection and try again.",
		})
		return errors.Join(err, statusErr)
	}
	if err := app.writeUIStatus(uiSendStatus{
		DocumentID: documentID,
		State:      "succeeded",
		Message:    "Notebook sent as a private handwritten.blog draft.",
	}); err != nil {
		return fmt.Errorf("record completed UI upload: %w", err)
	}
	return nil
}

func (app *application) uiResult(documentID string) error {
	if !validDocumentID(documentID) {
		return errors.New("document UUID must use the canonical 8-4-4-4-12 form")
	}
	status, err := app.readUIStatus(documentID)
	if errors.Is(err, os.ErrNotExist) {
		status = uiSendStatus{
			Version:    uiStatusVersion,
			DocumentID: documentID,
			State:      "idle",
		}
	} else if err != nil {
		return err
	}
	return json.NewEncoder(app.stdout).Encode(status)
}

func (app *application) uiAck(documentID string) error {
	if !validDocumentID(documentID) {
		return errors.New("document UUID must use the canonical 8-4-4-4-12 form")
	}
	status, err := app.readUIStatus(documentID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if status.State == "running" {
		return errors.New("cannot acknowledge an upload that is still running")
	}
	if err := os.Remove(app.uiStatusPath(documentID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (app *application) readUIStatus(documentID string) (uiSendStatus, error) {
	var status uiSendStatus
	contents, err := os.ReadFile(app.uiStatusPath(documentID))
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(contents, &status); err != nil {
		return status, fmt.Errorf("decode UI upload state: %w", err)
	}
	if status.Version != uiStatusVersion || status.DocumentID != documentID {
		return status, errors.New("UI upload state does not match the selected notebook")
	}
	return status, nil
}

func (app *application) writeUIStatus(status uiSendStatus) error {
	if !validDocumentID(status.DocumentID) {
		return errors.New("refusing to write UI state for an invalid document UUID")
	}
	if status.State != "running" && status.State != "succeeded" && status.State != "failed" {
		return errors.New("refusing to write an unknown UI upload state")
	}
	if err := app.ensureUICache(); err != nil {
		return err
	}
	status.Version = uiStatusVersion
	status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	contents, err := json.Marshal(status)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(app.cachePath, ".ui-send-status-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(contents, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, app.uiStatusPath(status.DocumentID))
}

func (app *application) ensureUICache() error {
	if err := os.MkdirAll(app.cachePath, 0o700); err != nil {
		return err
	}
	return os.Chmod(app.cachePath, 0o700)
}

func (app *application) uiStatusPath(documentID string) string {
	return filepath.Join(app.cachePath, "ui-send-status-"+documentID+".json")
}
