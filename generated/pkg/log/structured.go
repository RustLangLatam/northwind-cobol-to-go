package log

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// structured.go is the level filter and JSON output of the package logger. It is
// part of the skeleton every generated Go service ships with: a service configures
// it from LOG_LEVEL and LOG_FORMAT and tags each request, and never writes its own
// file in pkg/log (two services doing that is what broke migration 216's tree).
//
// Nothing changes until Configure is called: every level is printed, as text.

var (
	minRank    atomic.Int32 // the lowest rank printed; 0 prints everything
	jsonOutput atomic.Bool
	requestMap sync.Map // goroutine id -> requestContext
)

type requestContext struct{ requestID, traceID string }

// levelRank orders the levels; an unknown name is info.
func levelRank(level string) int32 {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		return 0
	case "WARN", WARNING:
		return 2
	case ERROR:
		return 3
	case FATAL:
		return 4
	default:
		return 1
	}
}

// Configure sets the lowest level printed ("debug", "info", "warning", "error") and
// the format: "text" keeps the colored line, anything else ("json", "") prints one
// JSON object per line.
func Configure(level, format string) {
	minRank.Store(levelRank(level))
	jsonOutput.Store(!strings.EqualFold(strings.TrimSpace(format), "text"))
}

// ConfigureFromEnv is Configure(LOG_LEVEL, LOG_FORMAT), defaulting to info and json.
func ConfigureFromEnv() {
	level := os.Getenv("LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	Configure(level, os.Getenv("LOG_FORMAT"))
}

// SetRequestContext tags every line the current goroutine logs with the request id
// and the trace id. Call ClearRequestContext when the request ends.
func SetRequestContext(requestID, traceID string) {
	requestMap.Store(getGoroutineID(), requestContext{requestID: requestID, traceID: traceID})
}

// ClearRequestContext removes the current goroutine's request tags.
func ClearRequestContext() {
	requestMap.Delete(getGoroutineID())
}

func currentRequest() requestContext {
	if v, ok := requestMap.Load(getGoroutineID()); ok {
		return v.(requestContext)
	}
	return requestContext{}
}

func enabled(level string) bool {
	return level == FATAL || levelRank(level) >= minRank.Load()
}

type jsonLine struct {
	Time      string `json:"ts"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"msg"`
	ContextID string `json:"ctx_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
}

// encodeJSONLine renders one log line as JSON, newline-terminated.
func encodeJSONLine(component, level, ctxID, msg string) []byte {
	rc := currentRequest()
	if ctxID == "unknown" {
		ctxID = ""
	}
	b, err := json.Marshal(jsonLine{
		Time: time.Now().UTC().Format(time.RFC3339Nano), Level: strings.ToLower(level),
		Component: component, Message: strings.TrimRight(msg, "\n"),
		ContextID: ctxID, RequestID: rc.requestID, TraceID: rc.traceID,
	})
	if err != nil { // a string-only struct always marshals; keep the line anyway
		b = []byte(`{"level":"error","msg":"log line could not be encoded"}`)
	}
	return append(b, '\n')
}
