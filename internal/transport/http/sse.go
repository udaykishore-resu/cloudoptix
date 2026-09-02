package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SSEWriter streams Server-Sent Events on the three long-running surfaces:
// onboarding chat turns, copilot answers, and job progress (discovery,
// analysis, execution). All three share this one writer rather than each
// handler formatting "data: ...\n\n" by hand, which is what keeps the
// framing (including the blank-line terminator SSE requires, and the flush
// after every event so a proxy buffering by default still delivers each
// event promptly) correct in exactly one place.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter prepares the response for event-stream output and returns a
// writer, or an error if the underlying ResponseWriter cannot be flushed
// incrementally (which would silently turn "streaming" into "buffer
// everything until the handler returns").
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("http: response writer does not support flushing, cannot stream SSE")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Disables buffering in nginx and similar reverse proxies that respect
	// this header — without it, "streaming" arrives as one burst when the
	// proxy's buffer fills or the connection closes, defeating the point.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSEWriter{w: w, flusher: flusher}, nil
}

// Event is one Server-Sent Event.
type Event struct {
	// ID, when set, lets a reconnecting client resume via Last-Event-ID —
	// job-progress streams set it to the step sequence number.
	ID    string
	Event string // the SSE "event" field; "" uses the default "message" type
	Data  any    // marshalled to JSON
}

// Send writes and flushes one event.
func (s *SSEWriter) Send(ev Event) error {
	body, err := json.Marshal(ev.Data)
	if err != nil {
		return fmt.Errorf("http: marshalling SSE event data: %w", err)
	}
	if ev.ID != "" {
		if _, err := fmt.Fprintf(s.w, "id: %s\n", ev.ID); err != nil {
			return err
		}
	}
	if ev.Event != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", ev.Event); err != nil {
			return err
		}
	}
	// A JSON payload never contains a literal newline, so a single "data:"
	// line is always sufficient — the multi-line "data:" framing the SSE
	// spec allows is not needed here.
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", body); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// SendError emits a terminal error event using the same Problem shape as a
// non-streaming failure, so a client's error handling code does not need a
// second error format just because the request happened to be streamed.
func (s *SSEWriter) SendError(err error) error {
	status, code, detail, _ := classify(err)
	return s.Send(Event{Event: "error", Data: map[string]any{
		"status": status, "code": code, "detail": detail,
	}})
}

// jobPollInterval is how often pollJobProgress re-fetches a job's state.
// One second is frequent enough to feel live in a UI and cheap enough that
// polling a snapshot-only service method (Get) instead of a native
// subscription is not a meaningful extra load on it. A var, not a const, so
// sse_test.go can shrink it for the duration of one test rather than a unit
// test paying wall-clock seconds to exercise the ticker-gated poll loop.
var jobPollInterval = time.Second

// pollJobProgress is the shared engine behind every "long-running job
// progress" SSE endpoint (discovery, analysis, execution): it repeatedly
// calls fetch, emits a "progress" event whenever the fetched snapshot's JSON
// encoding actually changed since the last poll (so a client watching the
// stream sees state transitions, not a redundant event every second), and
// emits one final "done" event once fetch reports completion. It returns
// when the client disconnects (r.Context() is done) or fetch reports done or
// errors.
//
// Polling a snapshot method is a deliberate, honestly-labelled choice, not a
// stand-in for a real push API: none of DiscoveryService, OptimizationService
// or AutomationService expose a subscription, only Get/GetPlan-style
// snapshots, so a push-based implementation here would have nothing to
// subscribe to. This is what the streaming surface those services can
// actually support looks like.
func pollJobProgress(r *http.Request, sse *SSEWriter, fetch func() (value any, done bool, err error)) {
	ticker := time.NewTicker(jobPollInterval)
	defer ticker.Stop()

	var lastEncoded string
	for {
		val, done, err := fetch()
		if err != nil {
			_ = sse.SendError(err)
			return
		}
		encoded, _ := json.Marshal(val)
		if string(encoded) != lastEncoded {
			event := "progress"
			if done {
				event = "done"
			}
			if sendErr := sse.Send(Event{Event: event, Data: val}); sendErr != nil {
				return // client disconnected mid-write
			}
			lastEncoded = string(encoded)
		}
		if done {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// Heartbeat sends a comment line (per the SSE spec, a line starting with ":"
// is ignored by the client but keeps the connection alive through
// intermediaries that time out an idle stream).
func (s *SSEWriter) Heartbeat() error {
	if _, err := fmt.Fprint(s.w, ": heartbeat\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}
