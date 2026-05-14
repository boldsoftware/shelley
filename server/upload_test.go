package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool/browse"
)

func TestUploadEndpoint(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	// Create a multipart form with a file
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create a test file
	part, err := writer.CreateFormFile("file", "test.png")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}

	// Write some fake PNG content (just the magic header bytes)
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if _, err := part.Write(pngData); err != nil {
		t.Fatalf("failed to write file content: %v", err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	server.handleUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	path, ok := response["path"]
	if !ok {
		t.Fatal("response missing 'path' field")
	}

	// Verify the path is in the upload directory
	if !strings.HasPrefix(path, browse.UploadDir) {
		t.Errorf("expected path to start with %s, got %s", browse.UploadDir, path)
	}

	// Verify the file has the correct extension
	if !strings.HasSuffix(path, ".png") {
		t.Errorf("expected path to end with .png, got %s", path)
	}

	// Verify the file exists and contains our data
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read uploaded file: %v", err)
	}

	if !bytes.Equal(data, pngData) {
		t.Errorf("uploaded file content mismatch")
	}

	// Clean up uploaded file
	os.Remove(path)
}

func TestUploadEndpointMethodNotAllowed(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/upload", nil)
	w := httptest.NewRecorder()

	server.handleUpload(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", w.Code)
	}
}

func TestUploadEndpointNoFile(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	// Create an empty multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	server.handleUpload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUploadRawEndpoint(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	body := []byte("raw video bytes")
	req := httptest.NewRequest("POST", "/api/upload/raw?filename=clip.mp4", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.handleUploadRaw(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	path := response["path"]
	defer os.Remove(path)

	if !strings.HasPrefix(path, browse.UploadDir) {
		t.Errorf("expected path to start with %s, got %s", browse.UploadDir, path)
	}
	if filepath.Ext(path) != ".mp4" {
		t.Errorf("expected .mp4 extension, got %q", filepath.Ext(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read uploaded file: %v", err)
	}
	if !bytes.Equal(data, body) {
		t.Errorf("uploaded file content mismatch")
	}
}

func TestUploadRawPreservesFilenameWhenSafe(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	unique := "raw-keep-" + filepath.Base(t.TempDir()) + ".bin"
	body := []byte("preserved-name body")
	req := httptest.NewRequest("POST", "/api/upload/raw?filename="+unique, bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.handleUploadRaw(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	path := response["path"]
	defer os.Remove(path)
	if got := filepath.Base(path); got != unique {
		t.Errorf("expected basename %q, got %q", unique, got)
	}
}

func TestUploadRawStripsPathTraversal(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	unique := "traversal-" + filepath.Base(t.TempDir()) + ".bin"
	hostile := "../../../" + unique
	req := httptest.NewRequest(
		"POST",
		"/api/upload/raw?filename="+url.QueryEscape(hostile),
		bytes.NewReader([]byte("ok")),
	)
	w := httptest.NewRecorder()

	server.handleUploadRaw(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	path := response["path"]
	defer os.Remove(path)
	if !strings.HasPrefix(path, browse.UploadDir+"/") {
		t.Errorf("upload escaped %s: %s", browse.UploadDir, path)
	}
	if filepath.Base(path) != unique {
		t.Errorf("expected basename %q, got %q", unique, filepath.Base(path))
	}
}

func TestUploadRawCollisionSuffix(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	name := "collide-" + filepath.Base(t.TempDir()) + ".bin"

	first := bytes.NewReader([]byte("first"))
	req1 := httptest.NewRequest("POST", "/api/upload/raw?filename="+name, first)
	w1 := httptest.NewRecorder()
	server.handleUploadRaw(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first upload failed: %s", w1.Body.String())
	}
	var resp1 map[string]string
	_ = json.Unmarshal(w1.Body.Bytes(), &resp1)
	defer os.Remove(resp1["path"])

	second := bytes.NewReader([]byte("second"))
	req2 := httptest.NewRequest("POST", "/api/upload/raw?filename="+name, second)
	w2 := httptest.NewRecorder()
	server.handleUploadRaw(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second upload failed: %s", w2.Body.String())
	}
	var resp2 map[string]string
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	defer os.Remove(resp2["path"])

	if resp1["path"] == resp2["path"] {
		t.Fatalf("collision should produce distinct paths; both got %q", resp1["path"])
	}
	if filepath.Ext(resp2["path"]) != filepath.Ext(name) {
		t.Errorf("expected extension %q preserved, got %q", filepath.Ext(name), filepath.Ext(resp2["path"]))
	}
	got, err := os.ReadFile(resp1["path"])
	if err != nil || string(got) != "first" {
		t.Errorf("first file content mismatch: %q, err %v", got, err)
	}
}

func TestUploadRawEndpointRequiresFilename(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/upload/raw", strings.NewReader("raw bytes"))
	w := httptest.NewRecorder()

	server.handleUploadRaw(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
	var response uploadErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if response.Error != "filename_required" || response.Message != "filename required" {
		t.Fatalf("unexpected error response: %+v", response)
	}
}

func TestUploadRoutesRawEndpoint(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	body := []byte("raw route bytes")
	req := httptest.NewRequest("POST", "/api/upload/raw?filename=route.bin", bytes.NewReader(body))
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	defer os.Remove(response["path"])
}

func TestUploadedFileCanBeReadViaReadEndpoint(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	// First, upload a file
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", "test.jpg")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}

	// Write some fake JPEG content
	jpgData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
	if _, err := part.Write(jpgData); err != nil {
		t.Fatalf("failed to write file content: %v", err)
	}
	writer.Close()

	uploadReq := httptest.NewRequest("POST", "/api/upload", body)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadW := httptest.NewRecorder()

	server.handleUpload(uploadW, uploadReq)

	if uploadW.Code != http.StatusOK {
		t.Fatalf("upload failed: %s", uploadW.Body.String())
	}

	var uploadResponse map[string]string
	if err := json.Unmarshal(uploadW.Body.Bytes(), &uploadResponse); err != nil {
		t.Fatalf("failed to parse upload response: %v", err)
	}

	path := uploadResponse["path"]

	// Now try to read the file via the read endpoint
	readReq := httptest.NewRequest("GET", "/api/read?path="+path, nil)
	readW := httptest.NewRecorder()

	server.handleRead(readW, readReq)

	if readW.Code != http.StatusOK {
		t.Fatalf("read failed with status %d: %s", readW.Code, readW.Body.String())
	}

	// Verify content type
	contentType := readW.Header().Get("Content-Type")
	if contentType != "image/jpeg" {
		t.Errorf("expected Content-Type image/jpeg, got %s", contentType)
	}

	// Verify content
	readData, err := io.ReadAll(readW.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if !bytes.Equal(readData, jpgData) {
		t.Errorf("read content mismatch")
	}

	// Clean up
	os.Remove(path)
}

func TestUploadPreservesFileExtension(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	testCases := []struct {
		filename string
		wantExt  string
	}{
		{"photo.png", ".png"},
		{"image.jpeg", ".jpeg"},
		{"screenshot.gif", ".gif"},
		{"document.pdf", ".pdf"},
		{"noextension", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)

			part, err := writer.CreateFormFile("file", tc.filename)
			if err != nil {
				t.Fatalf("failed to create form file: %v", err)
			}
			part.Write([]byte("test content"))
			writer.Close()

			req := httptest.NewRequest("POST", "/api/upload", body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			w := httptest.NewRecorder()

			server.handleUpload(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", w.Code)
			}

			var response map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}

			path := response["path"]
			ext := filepath.Ext(path)
			if ext != tc.wantExt {
				t.Errorf("expected extension %q, got %q", tc.wantExt, ext)
			}

			// Clean up
			os.Remove(path)
		})
	}
}

func TestUploadLargeFile(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	// 64 MiB — larger than the old 10 MiB cap, well under the 1 GiB cap.
	// This used to spill to a temp file via ParseMultipartForm and would have
	// been rejected with 400 "http: request body too large".
	const size = 64 * 1024 * 1024
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "clip.mp4")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	chunk := make([]byte, 64*1024)
	for written := 0; written < size; written += len(chunk) {
		if _, err := part.Write(chunk); err != nil {
			t.Fatalf("write chunk: %v", err)
		}
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	server.handleUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	os.Remove(response["path"])
}
