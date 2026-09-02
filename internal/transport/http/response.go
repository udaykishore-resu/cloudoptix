package http

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// WriteJSON renders v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON reads and decodes a JSON request body into a T, capping the
// read at maxBodyBytes worth of already-buffered content (the body-size-limit
// middleware has already wrapped r.Body, so this is a second, cheap guard
// against a handler that forgot the middleware ran) and rejecting unknown
// fields — a client sending a typo'd field name should learn that
// immediately, not have it silently ignored.
func DecodeJSON[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(io.LimitReader(r.Body, maxDecodeBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, core.Invalid("request body is not valid JSON: %s", err.Error())
	}
	return v, nil
}

// maxDecodeBytes is the decoder-level backstop; the real limit is enforced
// per-deployment by the body-size-limit middleware (middleware.go), which
// reads Config.Server.MaxRequestBytes.
const maxDecodeBytes = 10 << 20 // 10 MiB
