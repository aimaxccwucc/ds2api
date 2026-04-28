package files

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"ds2api/internal/auth"
	"ds2api/internal/chathistory"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/httpapi/openai/shared"
)

const openAIUploadMaxMemory = 32 << 20

type Handler struct {
	Store       shared.ConfigReader
	Auth        shared.AuthResolver
	DS          shared.DeepSeekCaller
	ChatHistory *chathistory.Store

	fileAccountsMu sync.Mutex
	fileAccounts   map[string]string
}

type uploadedFileFetcher interface {
	FetchUploadedFile(ctx context.Context, a *auth.RequestAuth, fileID string) (*dsclient.UploadFileResult, error)
}

func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request) {
	a, err := h.Auth.Determine(r)
	if err != nil {
		status := http.StatusUnauthorized
		detail := err.Error()
		if err == auth.ErrNoAccount {
			status = http.StatusTooManyRequests
		}
		shared.WriteOpenAIError(w, status, detail)
		return
	}
	defer func() {
		if a != nil {
			h.Auth.Release(a)
		}
	}()
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "multipart/form-data") {
		shared.WriteOpenAIError(w, http.StatusBadRequest, "content-type must be multipart/form-data")
		return
	}
	// Enforce a hard cap on the total request body size to prevent OOM
	r.Body = http.MaxBytesReader(w, r.Body, shared.UploadMaxSize)
	if err := r.ParseMultipartForm(openAIUploadMaxMemory); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			shared.WriteOpenAIError(w, http.StatusRequestEntityTooLarge, "file size exceeds limit")
			return
		}
		shared.WriteOpenAIError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	r = r.WithContext(auth.WithAuth(r.Context(), a))
	file, header, err := r.FormFile("file")
	if err != nil {
		shared.WriteOpenAIError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		shared.WriteOpenAIError(w, http.StatusBadRequest, "failed to read uploaded file")
		return
	}
	contentType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if contentType == "" && len(data) > 0 {
		contentType = http.DetectContentType(data)
	}
	result, err := h.DS.UploadFile(r.Context(), a, dsclient.UploadFileRequest{
		Filename:      header.Filename,
		ContentType:   contentType,
		Purpose:       strings.TrimSpace(r.FormValue("purpose")),
		Data:          data,
		SkipReadyWait: true,
	}, 3)
	if err != nil {
		shared.WriteOpenAIError(w, http.StatusInternalServerError, "Failed to upload file.")
		return
	}
	if result != nil && result.AccountID == "" {
		result.AccountID = a.AccountID
	}
	h.rememberFileAccount(result)
	shared.WriteJSON(w, http.StatusOK, buildOpenAIFileObject(result))
}

func (h *Handler) GetFile(w http.ResponseWriter, r *http.Request) {
	a, err := h.Auth.Determine(r)
	if err != nil {
		status := http.StatusUnauthorized
		detail := err.Error()
		if err == auth.ErrNoAccount {
			status = http.StatusTooManyRequests
		}
		shared.WriteOpenAIError(w, status, detail)
		return
	}
	defer h.Auth.Release(a)

	fileID := strings.TrimSpace(chi.URLParam(r, "file_id"))
	if fileID == "" {
		shared.WriteOpenAIError(w, http.StatusBadRequest, "file_id is required")
		return
	}
	if target := h.lookupFileAccount(fileID); target != "" && a.UseConfigToken && a.AccountID != target {
		h.Auth.Release(a)
		a = nil
		r.Header.Set("X-Ds2-Target-Account", target)
		a, err = h.Auth.Determine(r)
		if err != nil {
			status := http.StatusUnauthorized
			detail := err.Error()
			if err == auth.ErrNoAccount {
				status = http.StatusTooManyRequests
			}
			shared.WriteOpenAIError(w, status, detail)
			return
		}
	}
	fetcher, ok := h.DS.(uploadedFileFetcher)
	if !ok {
		shared.WriteOpenAIError(w, http.StatusNotImplemented, "File status lookup is not supported by this DeepSeek client.")
		return
	}
	result, err := fetcher.FetchUploadedFile(r.Context(), a, fileID)
	if err != nil {
		shared.WriteOpenAIError(w, http.StatusBadGateway, "Failed to retrieve file status.")
		return
	}
	if result != nil && result.AccountID == "" {
		result.AccountID = a.AccountID
	}
	h.rememberFileAccount(result)
	shared.WriteJSON(w, http.StatusOK, buildOpenAIFileObject(result))
}

func (h *Handler) rememberFileAccount(result *dsclient.UploadFileResult) {
	if h == nil || result == nil {
		return
	}
	fileID := strings.TrimSpace(result.ID)
	accountID := strings.TrimSpace(result.AccountID)
	if fileID == "" || accountID == "" {
		return
	}
	h.fileAccountsMu.Lock()
	defer h.fileAccountsMu.Unlock()
	if h.fileAccounts == nil {
		h.fileAccounts = map[string]string{}
	}
	h.fileAccounts[fileID] = accountID
}

func (h *Handler) lookupFileAccount(fileID string) string {
	if h == nil {
		return ""
	}
	h.fileAccountsMu.Lock()
	defer h.fileAccountsMu.Unlock()
	return strings.TrimSpace(h.fileAccounts[strings.TrimSpace(fileID)])
}

func buildOpenAIFileObject(result *dsclient.UploadFileResult) map[string]any {
	if result == nil {
		obj := map[string]any{
			"id":             "",
			"object":         "file",
			"bytes":          0,
			"created_at":     time.Now().Unix(),
			"filename":       "",
			"purpose":        "",
			"status":         "uploaded",
			"status_details": nil,
		}
		return obj
	}
	obj := map[string]any{
		"id":             result.ID,
		"object":         "file",
		"bytes":          result.Bytes,
		"created_at":     time.Now().Unix(),
		"filename":       result.Filename,
		"purpose":        result.Purpose,
		"status":         result.Status,
		"status_details": nil,
	}
	if result.AccountID != "" {
		obj["account_id"] = result.AccountID
	}
	return obj
}
