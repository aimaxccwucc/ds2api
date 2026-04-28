package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
)

type managedFilesAuthStub struct{}

func (managedFilesAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "managed-token",
		CallerID:       "caller:test",
		AccountID:      "acct-123",
		TriedAccounts:  map[string]bool{},
	}, nil
}

func (managedFilesAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "managed-token",
		CallerID:       "caller:test",
		AccountID:      "acct-123",
		TriedAccounts:  map[string]bool{},
	}, nil
}

func (managedFilesAuthStub) Release(_ *auth.RequestAuth) {}

type filesRouteDSStub struct {
	lastReq       dsclient.UploadFileRequest
	lastFetchAuth *auth.RequestAuth
	upload        *dsclient.UploadFileResult
	fetched       *dsclient.UploadFileResult
	err           error
}

func (m *filesRouteDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "", nil
}

func (m *filesRouteDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "", nil
}

func (m *filesRouteDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, req dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	if m.upload != nil {
		return m.upload, nil
	}
	return &dsclient.UploadFileResult{ID: "file-123", Filename: req.Filename, Bytes: int64(len(req.Data)), Purpose: req.Purpose, Status: "uploaded"}, nil
}

func (m *filesRouteDSStub) FetchUploadedFile(_ context.Context, a *auth.RequestAuth, fileID string) (*dsclient.UploadFileResult, error) {
	m.lastFetchAuth = a
	if m.err != nil {
		return nil, m.err
	}
	if m.fetched != nil {
		return m.fetched, nil
	}
	return &dsclient.UploadFileResult{ID: fileID, Filename: "notes.txt", Bytes: 11, Purpose: "assistants", Status: "processed"}, nil
}

type rotatingManagedFilesAuthStub struct {
	accounts []string
	calls    int
	released []string
}

func (m *rotatingManagedFilesAuthStub) Determine(req *http.Request) (*auth.RequestAuth, error) {
	target := strings.TrimSpace(req.Header.Get("X-Ds2-Target-Account"))
	accountID := target
	if accountID == "" {
		if m.calls < len(m.accounts) {
			accountID = m.accounts[m.calls]
		} else if len(m.accounts) > 0 {
			accountID = m.accounts[len(m.accounts)-1]
		}
	}
	m.calls++
	return &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "managed-token",
		CallerID:       "caller:test",
		AccountID:      accountID,
		TriedAccounts:  map[string]bool{},
	}, nil
}

func (m *rotatingManagedFilesAuthStub) DetermineCaller(req *http.Request) (*auth.RequestAuth, error) {
	return m.Determine(req)
}

func (m *rotatingManagedFilesAuthStub) Release(a *auth.RequestAuth) {
	if a != nil {
		m.released = append(m.released, a.AccountID)
	}
}

func (m *filesRouteDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (m *filesRouteDSStub) DeleteSessionForToken(_ context.Context, _ string, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (m *filesRouteDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func newMultipartUploadRequest(t *testing.T, purpose string, filename string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if purpose != "" {
		if err := writer.WriteField("purpose", purpose); err != nil {
			t.Fatalf("write purpose failed: %v", err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file failed: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write file failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestFilesRouteUploadSuccess(t *testing.T) {
	ds := &filesRouteDSStub{}
	h := &openAITestSurface{Store: mockOpenAIConfig{wideInput: true}, Auth: streamStatusAuthStub{}, DS: ds}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)

	req := newMultipartUploadRequest(t, "assistants", "notes.txt", []byte("hello world"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ds.lastReq.Filename != "notes.txt" {
		t.Fatalf("expected filename notes.txt, got %q", ds.lastReq.Filename)
	}
	if ds.lastReq.Purpose != "assistants" {
		t.Fatalf("expected purpose assistants, got %q", ds.lastReq.Purpose)
	}
	if string(ds.lastReq.Data) != "hello world" {
		t.Fatalf("unexpected uploaded data: %q", string(ds.lastReq.Data))
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, rec.Body.String())
	}
	if out["object"] != "file" {
		t.Fatalf("expected file object, got %#v", out)
	}
	if out["id"] != "file-123" {
		t.Fatalf("expected file id file-123, got %#v", out["id"])
	}
	if out["filename"] != "notes.txt" {
		t.Fatalf("expected filename notes.txt, got %#v", out["filename"])
	}
}

func TestFilesRouteUploadSkipsReadyWait(t *testing.T) {
	ds := &filesRouteDSStub{}
	h := &openAITestSurface{Store: mockOpenAIConfig{wideInput: true}, Auth: streamStatusAuthStub{}, DS: ds}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)

	req := newMultipartUploadRequest(t, "vision", "image.png", []byte("image-data"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !ds.lastReq.SkipReadyWait {
		t.Fatalf("expected /v1/files upload to skip readiness polling")
	}
}

func TestFilesRouteGetFileStatusSuccess(t *testing.T) {
	ds := &filesRouteDSStub{fetched: &dsclient.UploadFileResult{ID: "file-123", Filename: "image.png", Bytes: 68, Purpose: "vision", Status: "PENDING"}}
	h := &openAITestSurface{Store: mockOpenAIConfig{wideInput: true}, Auth: streamStatusAuthStub{}, DS: ds}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/v1/files/file-123", nil)
	req.Header.Set("Authorization", "Bearer direct-token")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, rec.Body.String())
	}
	if out["id"] != "file-123" || out["status"] != "PENDING" {
		t.Fatalf("unexpected file object: %#v", out)
	}
}

func TestFilesRouteGetFileStatusReusesUploadedAccount(t *testing.T) {
	ds := &filesRouteDSStub{
		upload:  &dsclient.UploadFileResult{ID: "file-123", Filename: "image.png", Bytes: 68, Purpose: "vision", Status: "PENDING"},
		fetched: &dsclient.UploadFileResult{ID: "file-123", Filename: "image.png", Bytes: 68, Purpose: "vision", Status: "SUCCESS"},
	}
	authStub := &rotatingManagedFilesAuthStub{accounts: []string{"acct-upload", "acct-other"}}
	h := &openAITestSurface{Store: mockOpenAIConfig{wideInput: true}, Auth: authStub, DS: ds}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)

	uploadReq := newMultipartUploadRequest(t, "vision", "image.png", []byte("image-data"))
	uploadRec := httptest.NewRecorder()
	r.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d body=%s", uploadRec.Code, uploadRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/files/file-123", nil)
	getReq.Header.Set("Authorization", "Bearer managed-key")
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected get 200, got %d body=%s", getRec.Code, getRec.Body.String())
	}
	if ds.lastFetchAuth == nil || ds.lastFetchAuth.AccountID != "acct-upload" {
		t.Fatalf("expected file lookup to reuse uploaded account, got %#v", ds.lastFetchAuth)
	}
	if len(authStub.released) != 3 {
		t.Fatalf("expected release for upload, initial get account, and target get account; got %#v", authStub.released)
	}
}

func TestFilesRouteUploadIncludesAccountIDForManagedAccount(t *testing.T) {
	ds := &filesRouteDSStub{}
	h := &openAITestSurface{Store: mockOpenAIConfig{wideInput: true}, Auth: managedFilesAuthStub{}, DS: ds}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)

	req := newMultipartUploadRequest(t, "assistants", "notes.txt", []byte("hello world"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, rec.Body.String())
	}
	if out["account_id"] != "acct-123" {
		t.Fatalf("expected account_id acct-123, got %#v", out["account_id"])
	}
}

func TestFilesRouteRejectsNonMultipart(t *testing.T) {
	h := &openAITestSurface{Store: mockOpenAIConfig{wideInput: true}, Auth: streamStatusAuthStub{}, DS: &filesRouteDSStub{}}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", bytes.NewBufferString(`{"purpose":"assistants"}`))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFilesRouteRequiresFileField(t *testing.T) {
	h := &openAITestSurface{Store: mockOpenAIConfig{wideInput: true}, Auth: streamStatusAuthStub{}, DS: &filesRouteDSStub{}}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", "assistants"); err != nil {
		t.Fatalf("write field failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
