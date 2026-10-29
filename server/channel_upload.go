package server

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/claudetool"
)

// maxChannelAttachmentBytes is Linq's pre-upload limit.
const maxChannelAttachmentBytes = 100 << 20

func (s *Server) uploadChannelAttachment(ctx context.Context, endpoint string, attachment claudetool.MessageUserAttachment) (string, error) {
	f, err := os.Open(attachment.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() <= 0 || fi.Size() > maxChannelAttachmentBytes {
		return "", fmt.Errorf("attachment must be a non-empty regular file of at most 100 MiB")
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	contentType := http.DetectContentType(head[:n])
	// Go's sniffer does not recognize TIFF or HEIC. Keep known sniffed
	// types, but refine generic binary metadata using the file's extension.
	if contentType == "application/octet-stream" {
		ext := strings.ToLower(filepath.Ext(fi.Name()))
		switch ext {
		case ".heic":
			contentType = "image/heic"
		case ".heif":
			contentType = "image/heif"
		default:
			if known := mime.TypeByExtension(ext); known != "" {
				contentType = known
			}
		}
	}
	contentType, _, err = mime.ParseMediaType(contentType)
	if err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	meta := map[string]any{"filename": fi.Name(), "content_type": contentType, "size_bytes": fi.Size()}
	var upload struct {
		AttachmentID    string            `json:"attachment_id"`
		UploadURL       string            `json:"upload_url"`
		RequiredHeaders map[string]string `json:"required_headers"`
	}
	if err := s.callChannelEndpoint(ctx, endpoint, "/attachments", meta, &upload); err != nil {
		return "", err
	}
	if upload.AttachmentID == "" || upload.UploadURL == "" {
		return "", fmt.Errorf("provider returned incomplete upload metadata")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, upload.UploadURL, f)
	if err != nil {
		return "", err
	}
	req.ContentLength = fi.Size()
	for name, value := range upload.RequiredHeaders {
		req.Header.Set(name, value)
	}
	// Signed storage URLs are exact PUT destinations. Following a 302/303
	// would turn the upload into a GET and could acknowledge missing bytes.
	client := *s.channelClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("storage returned HTTP %d", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)); err != nil {
		return "", err
	}
	return upload.AttachmentID, nil
}
