package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	version = "development"
	baseURL = "https://handwritten.blog"
)

type application struct {
	api        *apiClient
	configPath string
	rootPath   string
	cachePath  string
	service    serviceController
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	sleep      func(context.Context, time.Duration) error
}

func newApplication() *application {
	return &application{
		api:        newAPIClient(baseURL),
		configPath: "/home/root/.config/handwritten-blog/config.json",
		rootPath:   "/home/root/.local/share/remarkable/xochitl",
		cachePath:  "/home/root/.cache/handwritten-blog",
		service:    systemdService{name: "xochitl"},
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		sleep:      sleepContext,
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newApplication().run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "handwritten-blog: %v\n", err)
		os.Exit(1)
	}
}

func (app *application) run(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		app.printUsage()
		return nil
	}

	switch arguments[0] {
	case "link":
		return app.link(ctx)
	case "list":
		if len(arguments) > 2 || (len(arguments) == 2 && arguments[1] != "--json") {
			return errors.New("usage: handwritten-blog list [--json]")
		}
		return app.list(ctx, len(arguments) == 2)
	case "sync":
		if len(arguments) > 2 {
			return errors.New("usage: handwritten-blog sync [document-uuid]")
		}
		documentID := ""
		if len(arguments) == 2 {
			documentID = strings.ToLower(arguments[1])
		} else {
			var err error
			documentID, err = app.selectNotebook(ctx)
			if err != nil {
				return err
			}
		}
		return app.sync(ctx, documentID)
	case "status":
		if len(arguments) > 2 {
			return errors.New("usage: handwritten-blog status [document-uuid]")
		}
		documentID := ""
		if len(arguments) == 2 {
			documentID = strings.ToLower(arguments[1])
		}
		return app.status(ctx, documentID)
	case "unlink":
		return app.unlink(ctx)
	case "purge":
		return app.purge()
	case "version", "--version", "-v":
		fmt.Fprintln(app.stdout, version)
		return nil
	case "help", "--help", "-h":
		app.printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q; run handwritten-blog help", arguments[0])
	}
}

func (app *application) link(ctx context.Context) error {
	if _, err := loadConfig(app.configPath); err == nil {
		return errors.New("this tablet is already linked; run handwritten-blog unlink first")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "reMarkable"
	}
	authorization, err := app.api.createAuthorization(ctx, hostname)
	if err != nil {
		return err
	}

	fmt.Fprintf(app.stdout, "Open %s\n", authorization.VerificationURI)
	fmt.Fprintf(app.stdout, "Enter code: %s\n", authorization.UserCode)
	fmt.Fprintln(app.stdout, "Waiting for approval…")

	deadline := time.Now().Add(time.Duration(authorization.ExpiresIn) * time.Second)
	interval := time.Duration(authorization.Interval) * time.Second
	for time.Now().Before(deadline) {
		issued, pending, err := app.api.pollToken(ctx, authorization.DeviceCode)
		if err != nil {
			return err
		}
		if !pending {
			configuration := deviceConfig{AccessToken: issued.AccessToken, DeviceID: issued.DeviceID}
			if err := saveConfig(app.configPath, configuration); err != nil {
				return err
			}
			fmt.Fprintln(app.stdout, "Linked. Run handwritten-blog sync to choose and send a notebook.")
			return nil
		}
		if err := app.sleep(ctx, interval); err != nil {
			return err
		}
	}
	return errors.New("the link code expired; run handwritten-blog link again")
}

func (app *application) sync(ctx context.Context, documentID string) error {
	if !validDocumentID(documentID) {
		return errors.New("document UUID must use the canonical 8-4-4-4-12 form")
	}
	configuration, err := loadConfig(app.configPath)
	if err != nil {
		return fmt.Errorf("load linked device: %w", err)
	}

	snapshotter := notebookSnapshotter{
		rootPath:  app.rootPath,
		cachePath: app.cachePath,
		service:   app.service,
	}
	snapshot, err := snapshotter.create(ctx, documentID)
	if err != nil {
		return err
	}
	defer snapshot.cleanup()

	fmt.Fprintf(app.stdout, "Uploading %q…\n", snapshot.title)
	result, err := app.api.upload(ctx, configuration, snapshot)
	if err != nil {
		return err
	}
	state := result.Result
	if state == "" {
		state = result.State
	}
	fmt.Fprintf(app.stdout, "%s: %s\n", result.Title, strings.ReplaceAll(state, "_", " "))
	return nil
}

type notebookListOutput struct {
	Version   int                `json:"version"`
	Notebooks []notebookListItem `json:"notebooks"`
}

type notebookListItem struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
}

func (app *application) list(ctx context.Context, jsonOutput bool) error {
	snapshotter := notebookSnapshotter{
		rootPath:  app.rootPath,
		cachePath: app.cachePath,
		service:   app.service,
	}
	notebooks, err := snapshotter.list(ctx)
	if err != nil {
		return err
	}
	if jsonOutput {
		output := notebookListOutput{
			Version:   1,
			Notebooks: make([]notebookListItem, 0, len(notebooks)),
		}
		for _, notebook := range notebooks {
			output.Notebooks = append(output.Notebooks, notebookListItem{
				DocumentID: notebook.documentID,
				Title:      notebook.title,
			})
		}
		return json.NewEncoder(app.stdout).Encode(output)
	}
	if len(notebooks) == 0 {
		fmt.Fprintln(app.stdout, "No native notebooks found.")
		return nil
	}
	for _, notebook := range notebooks {
		fmt.Fprintf(app.stdout, "%s  %s\n", notebook.documentID, notebook.title)
	}
	return nil
}

func (app *application) selectNotebook(ctx context.Context) (string, error) {
	snapshotter := notebookSnapshotter{
		rootPath:  app.rootPath,
		cachePath: app.cachePath,
		service:   app.service,
	}
	notebooks, err := snapshotter.list(ctx)
	if err != nil {
		return "", err
	}
	if len(notebooks) == 0 {
		return "", errors.New("no local native notebooks found")
	}

	for index, notebook := range notebooks {
		fmt.Fprintf(app.stdout, "%d. %s\n", index+1, notebook.title)
	}
	fmt.Fprintf(app.stdout, "Choose a notebook [1-%d]: ", len(notebooks))
	reader := app.stdin
	if reader == nil {
		reader = os.Stdin
	}
	answer, readErr := bufio.NewReader(reader).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("read notebook selection: %w", readErr)
	}
	selection, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || selection < 1 || selection > len(notebooks) {
		return "", fmt.Errorf("choose a number from 1 to %d", len(notebooks))
	}
	return notebooks[selection-1].documentID, nil
}

func (app *application) status(ctx context.Context, documentID string) error {
	configuration, err := loadConfig(app.configPath)
	if err != nil {
		return fmt.Errorf("load linked device: %w", err)
	}
	if documentID == "" {
		fmt.Fprintf(app.stdout, "Linked device %s\n", configuration.DeviceID)
		return nil
	}
	if !validDocumentID(documentID) {
		return errors.New("document UUID must use the canonical 8-4-4-4-12 form")
	}

	result, err := app.api.documentStatus(ctx, configuration, documentID)
	if err != nil {
		return err
	}
	fmt.Fprintf(app.stdout, "%s: %s\n", result.Title, strings.ReplaceAll(result.State, "_", " "))
	return nil
}

func (app *application) unlink(ctx context.Context) error {
	configuration, err := loadConfig(app.configPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(app.stdout, "This tablet is not linked.")
		return nil
	}
	if err != nil {
		return err
	}

	if err := app.api.revoke(ctx, configuration); err != nil {
		var remote *apiError
		if !errors.As(err, &remote) || remote.StatusCode != 401 {
			return fmt.Errorf("revoke device before removing its credential: %w", err)
		}
	}
	if err := os.Remove(app.configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(app.stdout, "Disconnected. Existing drafts and posts were kept.")
	return nil
}

func (app *application) purge() error {
	if err := os.Remove(app.configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(app.cachePath); err != nil {
		return err
	}
	fmt.Fprintln(app.stdout, "Removed the local credential and temporary uploader data.")
	return nil
}

func (app *application) printUsage() {
	fmt.Fprintln(app.stdout, `Usage: handwritten-blog <command>

Commands:
  link                 Link this tablet to one handwritten.blog
  list [--json]        List local native notebooks and their UUIDs
  sync [document-uuid] Choose and upload one native notebook, or pass its UUID
  status [uuid]        Show link or uploaded-notebook status
  unlink               Revoke and remove the device credential
  purge                Remove local credential and temporary data
  version              Print the uploader version`)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
