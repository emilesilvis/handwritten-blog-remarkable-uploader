package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

type apiClient struct {
	baseURL string
	http    *http.Client
}

type deviceAuthorizationResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type deviceTokenResponse struct {
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
}

type documentResponse struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Result     string `json:"result"`
}

type apiError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *apiError) Error() string {
	if err.Message != "" {
		return err.Message
	}
	return fmt.Sprintf("handwritten.blog returned HTTP %d", err.StatusCode)
}

func newAPIClient(origin string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(origin, "/"),
		http: &http.Client{
			Timeout: 3 * time.Minute,
		},
	}
}

func (client *apiClient) createAuthorization(ctx context.Context, deviceName string) (deviceAuthorizationResponse, error) {
	var result deviceAuthorizationResponse
	err := client.jsonRequest(ctx, http.MethodPost, "/api/remarkable/device_authorization", "", map[string]string{
		"device_name": deviceName,
	}, &result)
	return result, err
}

func (client *apiClient) pollToken(ctx context.Context, deviceCode string) (deviceTokenResponse, bool, error) {
	var result deviceTokenResponse
	err := client.jsonRequest(ctx, http.MethodPost, "/api/remarkable/device_token", "", map[string]string{
		"device_code": deviceCode,
	}, &result)
	if remote, ok := err.(*apiError); ok && remote.Code == "authorization_pending" {
		return result, true, nil
	}
	return result, false, err
}

func (client *apiClient) documentStatus(ctx context.Context, configuration deviceConfig, documentID string) (documentResponse, error) {
	var result documentResponse
	endpoint := path.Join("/api/remarkable/documents", url.PathEscape(documentID))
	err := client.jsonRequest(ctx, http.MethodGet, endpoint, configuration.AccessToken, nil, &result)
	return result, err
}

func (client *apiClient) revoke(ctx context.Context, configuration deviceConfig) error {
	return client.jsonRequest(ctx, http.MethodDelete, "/api/remarkable/device", configuration.AccessToken, nil, nil)
}

func (client *apiClient) upload(ctx context.Context, configuration deviceConfig, snapshot *notebookSnapshot) (documentResponse, error) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	writeError := make(chan error, 1)
	go func() {
		defer close(writeError)
		defer writer.Close()
		if err := multipartWriter.WriteField("document_id", snapshot.documentID); err != nil {
			writeError <- err
			return
		}
		if err := multipartWriter.WriteField("content_hash", snapshot.contentHash); err != nil {
			writeError <- err
			return
		}
		if err := multipartWriter.WriteField("title", snapshot.title); err != nil {
			writeError <- err
			return
		}
		part, err := multipartWriter.CreateFormFile("archive", "notebook.rmdoc")
		if err != nil {
			writeError <- err
			return
		}
		archive, err := os.Open(snapshot.archivePath)
		if err != nil {
			writeError <- err
			return
		}
		_, copyErr := io.Copy(part, archive)
		closeErr := archive.Close()
		if copyErr != nil {
			writeError <- copyErr
			return
		}
		if closeErr != nil {
			writeError <- closeErr
			return
		}
		if err := multipartWriter.Close(); err != nil {
			writeError <- err
			return
		}
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/api/remarkable/uploads", reader)
	if err != nil {
		return documentResponse{}, err
	}
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+configuration.AccessToken)
	request.Header.Set("Idempotency-Key", idempotencyKey(snapshot.documentID, snapshot.contentHash))

	var result documentResponse
	err = client.do(request, &result)
	if writeErr := <-writeError; err == nil && writeErr != nil {
		err = writeErr
	}
	return result, err
}

func (client *apiClient) jsonRequest(ctx context.Context, method, endpoint, token string, body any, result any) error {
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+endpoint, input)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return client.do(request, result)
}

func (client *apiClient) do(request *http.Request, result any) error {
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("handwritten.blog returned an oversized response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		remote := &apiError{StatusCode: response.StatusCode}
		_ = json.Unmarshal(body, &struct {
			Code    *string `json:"error"`
			Message *string `json:"message"`
		}{Code: &remote.Code, Message: &remote.Message})
		return remote
	}
	if result != nil && len(body) > 0 {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("decode handwritten.blog response: %w", err)
		}
	}
	return nil
}
