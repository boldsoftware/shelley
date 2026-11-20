package server

import (
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"

	sloghttp "github.com/samber/slog-http"
)

// LoggerMiddleware adds request logging using slog-http
func LoggerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	config := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
		WithRequestID:    false,
		// The callback's query holds an authorization code.
		Filters: []sloghttp.Filter{sloghttp.IgnorePath("/mcp/oauth/callback")},
	}
	return sloghttp.NewWithConfig(logger, config)
}

// RequireHeaderMiddleware requires a specific header to be present on API
// requests and the MCP login and debug pages. This is used to ensure requests
// come through an authenticated proxy.
func RequireHeaderMiddleware(headerName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/mcp/") || p == "/debug/mcp" {
				if r.Header.Get(headerName) == "" {
					http.Error(w, "missing required header: "+headerName, http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// compressedResponseWriter compresses non-streaming HTTP responses.
type compressedResponseWriter struct {
	http.ResponseWriter
	writer io.Writer
}

func (w *compressedResponseWriter) Write(b []byte) (int, error) {
	w.Header().Del("Content-Length")
	return w.writer.Write(b)
}

func (w *compressedResponseWriter) WriteHeader(statusCode int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(statusCode)
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		gw, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
		return gw
	},
}

var zstdWriterPool = sync.Pool{
	New: func() any {
		zw, err := zstd.NewWriter(
			nil,
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderConcurrency(1),
		)
		if err != nil {
			panic(err)
		}
		return zw
	},
}

// compressionHandler compresses non-streaming responses with zstd when
// available, falling back to gzip or identity.
func compressionHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		encoding, acceptable := negotiateContentEncoding(r)
		if !acceptable {
			http.Error(w, "no acceptable encoding", http.StatusNotAcceptable)
			return
		}

		var writer io.Writer
		switch encoding {
		case "zstd":
			zw := zstdWriterPool.Get().(*zstd.Encoder)
			zw.Reset(w)
			defer func() {
				if zw.Close() == nil {
					zstdWriterPool.Put(zw)
				}
			}()
			writer = zw
		case "gzip":
			gw := gzipWriterPool.Get().(*gzip.Writer)
			gw.Reset(w)
			defer func() {
				if gw.Close() == nil {
					gzipWriterPool.Put(gw)
				}
			}()
			writer = gw
		default:
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Encoding", encoding)
		w.Header().Del("Content-Length")
		next.ServeHTTP(&compressedResponseWriter{ResponseWriter: w, writer: writer}, r)
	})
}

// negotiateContentEncoding prefers zstd, then gzip, then identity. It honors
// Accept-Encoding q=0 exclusions and returns false when identity is also
// explicitly refused.
func negotiateContentEncoding(r *http.Request) (string, bool) {
	ae := r.Header.Get("Accept-Encoding")
	if strings.TrimSpace(ae) == "" {
		return "", true
	}
	prefs := parseAcceptEncoding(ae)
	codingAcceptable := func(name string) bool {
		if q, ok := prefs[name]; ok {
			return q > 0
		}
		if q, ok := prefs["*"]; ok {
			return q > 0
		}
		return false
	}
	if codingAcceptable("zstd") {
		return "zstd", true
	}
	if codingAcceptable("gzip") {
		return "gzip", true
	}
	if q, ok := prefs["identity"]; ok {
		return "", q > 0
	}
	if q, ok := prefs["*"]; ok && q == 0 {
		return "", false
	}
	return "", true
}

func parseAcceptEncoding(h string) map[string]float64 {
	out := map[string]float64{}
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		if coding == "" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			name, val, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || strings.ToLower(strings.TrimSpace(name)) != "q" {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				q = parsed
			}
		}
		out[coding] = q
	}
	return out
}
