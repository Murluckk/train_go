package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"
)

// errorBody is the single error shape the whole API speaks.
type errorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, body any) {
	buf, err := json.Marshal(body)
	if err != nil {
		log.ErrorContext(r.Context(), "marshal response", "path", r.URL.Path, "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(buf); err != nil {
		log.DebugContext(r.Context(), "write response", "path", r.URL.Path, "error", err)
	}
}

// writeError sends a status and a message. The underlying error is logged but
// only echoed to the client for statuses that describe the request itself:
// this is a local single user tool, but leaking a raw database error into the
// UI still helps nobody.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, msg string, err error) {
	body := errorBody{Error: msg}
	if err != nil {
		if status < http.StatusInternalServerError {
			body.Detail = err.Error()
		}
		log.ErrorContext(r.Context(), "request failed",
			"method", r.Method, "path", r.URL.Path, "status", status, "message", msg, "error", err)
	}
	writeJSON(w, r, log, status, body)
}

// decodeJSON reads a request body into v, rejecting unknown fields so that a
// typo in the client is caught rather than silently ignored.
func decodeJSON(r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parse request body: %w", err)
	}
	return nil
}

func pathInt64(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", name, raw)
	}
	return n, nil
}

func queryInt(r *http.Request, name string, def int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// logRequests records one line per request at debug level, and anything slow
// or failing at info.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelDebug
		if rec.status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		s.deps.Logger.Log(r.Context(), level, "request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(started))
	})
}

// recoverPanics keeps one bad handler from taking the session down mid-task.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				// A client that went away mid-stream is not an error worth
				// logging, and net/http wants the panic to propagate.
				panic(p)
			}
			s.deps.Logger.ErrorContext(r.Context(), "handler panicked",
				"method", r.Method, "path", r.URL.Path, "panic", p, "stack", string(debug.Stack()))
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Flush forwards to the wrapped writer so that SSE keeps streaming through the
// logging middleware.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		if !r.wroteHeader {
			r.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
