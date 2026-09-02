package http

import (
	"bufio"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewSSEWriter_SetsEventStreamHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := NewSSEWriter(rec)
	require.NoError(t, err)
	require.NotNil(t, sse)

	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	require.Equal(t, "keep-alive", rec.Header().Get("Connection"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.Equal(t, http.StatusOK, rec.Code)
}

// nonFlushingWriter implements http.ResponseWriter but deliberately not
// http.Flusher, to exercise NewSSEWriter's refusal path.
type nonFlushingWriter struct{ http.ResponseWriter }

func TestNewSSEWriter_RefusesWhenNotFlushable(t *testing.T) {
	_, err := NewSSEWriter(nonFlushingWriter{httptest.NewRecorder()})
	require.Error(t, err)
}

func TestSSEWriter_Send_FramesEventCorrectly(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := NewSSEWriter(rec)
	require.NoError(t, err)

	require.NoError(t, sse.Send(Event{ID: "1", Event: "progress", Data: map[string]any{"state": "running"}}))

	body := rec.Body.String()
	require.Contains(t, body, "id: 1\n")
	require.Contains(t, body, "event: progress\n")
	require.Contains(t, body, `data: {"state":"running"}`)
	require.True(t, strings.HasSuffix(body, "\n\n"), "every SSE event must end with a blank-line terminator")
}

func TestSSEWriter_Send_DefaultEventTypeOmitsEventLine(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := NewSSEWriter(rec)
	require.NoError(t, err)
	require.NoError(t, sse.Send(Event{Data: "hello"}))
	require.NotContains(t, rec.Body.String(), "event:")
}

func TestSSEWriter_SendError_UsesProblemClassification(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := NewSSEWriter(rec)
	require.NoError(t, err)
	require.NoError(t, sse.SendError(errors.New("boom")))

	body := rec.Body.String()
	require.Contains(t, body, "event: error\n")
	require.Contains(t, body, `"status":500`)
}

func TestSSEWriter_Heartbeat_IsACommentLine(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := NewSSEWriter(rec)
	require.NoError(t, err)
	require.NoError(t, sse.Heartbeat())
	require.True(t, strings.HasPrefix(rec.Body.String(), ":"))
}

// TestPollJobProgress_EmitsOnlyOnChangeAndStopsOnDone drives pollJobProgress
// with a fetch stub that returns the same value twice (no event expected the
// second time) before completing, and checks it emits exactly one
// "progress" event followed by one "done" event and then returns. It shrinks
// jobPollInterval for its duration so the test does not pay real wall-clock
// seconds for the ticker-gated poll loop.
func TestPollJobProgress_EmitsOnlyOnChangeAndStopsOnDone(t *testing.T) {
	prevInterval := jobPollInterval
	jobPollInterval = time.Millisecond
	t.Cleanup(func() { jobPollInterval = prevInterval })

	rec := httptest.NewRecorder()
	sse, err := NewSSEWriter(rec)
	require.NoError(t, err)

	calls := 0
	fetch := func() (any, bool, error) {
		calls++
		switch calls {
		case 1:
			return map[string]any{"state": "running"}, false, nil
		case 2:
			return map[string]any{"state": "running"}, false, nil // unchanged: no new event
		default:
			return map[string]any{"state": "completed"}, true, nil // done: terminal event
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	done := make(chan struct{})
	go func() {
		pollJobProgress(req, sse, fetch)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollJobProgress did not return after fetch reported done")
	}

	require.GreaterOrEqual(t, calls, 3, "fetch should have run at least through the done call")

	scanner := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	var eventLines []string
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "event:") {
			eventLines = append(eventLines, line)
		}
	}
	// Exactly two frames: the first "running" fetch (a state change from the
	// zero value) rendered as "progress", and the terminal "completed" fetch
	// rendered as "done" — the unchanged second fetch produces no frame at
	// all, which is the point of the encoded-value dedup in pollJobProgress.
	require.Equal(t, []string{"event: progress", "event: done"}, eventLines)
}
