package otel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The OTLP/HTTP JSON encoding, as far as yardr writes it: ids are hex
// strings, 64-bit integers are decimal strings, enums are numbers
// (https://opentelemetry.io/docs/specs/otlp/#json-protobuf-encoding).

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type anyValue struct {
	String *string `json:"stringValue,omitempty"`
	Int    *string `json:"intValue,omitempty"`
}

func strAttr(k, v string) keyValue { return keyValue{k, anyValue{String: &v}} }

func intAttr(k string, v uint64) keyValue {
	s := strconv.FormatUint(v, 10)
	return keyValue{k, anyValue{Int: &s}}
}

type resource struct {
	Attributes []keyValue `json:"attributes"`
}

type scope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type span struct {
	TraceID      string     `json:"traceId"`
	SpanID       string     `json:"spanId"`
	ParentSpanID string     `json:"parentSpanId,omitempty"`
	Name         string     `json:"name"`
	Kind         int        `json:"kind"`
	Start        string     `json:"startTimeUnixNano"`
	End          string     `json:"endTimeUnixNano"`
	Attributes   []keyValue `json:"attributes,omitempty"`
	Links        []link     `json:"links,omitempty"`
	Status       *status    `json:"status,omitempty"`
}

type link struct {
	TraceID    string     `json:"traceId"`
	SpanID     string     `json:"spanId"`
	Attributes []keyValue `json:"attributes,omitempty"`
}

type status struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

const (
	spanInternal = 1 // SPAN_KIND_INTERNAL
	statusError  = 2 // STATUS_CODE_ERROR
	severityInfo = 9 // SEVERITY_NUMBER_INFO
)

type logRecord struct {
	Time           string     `json:"timeUnixNano"`
	Observed       string     `json:"observedTimeUnixNano"`
	SeverityNumber int        `json:"severityNumber"`
	SeverityText   string     `json:"severityText"`
	Body           anyValue   `json:"body"`
	Attributes     []keyValue `json:"attributes,omitempty"`
	TraceID        string     `json:"traceId,omitempty"`
}

type scopeSpans struct {
	Scope scope  `json:"scope"`
	Spans []span `json:"spans"`
}

type resourceSpans struct {
	Resource   resource     `json:"resource"`
	ScopeSpans []scopeSpans `json:"scopeSpans"`
}

type tracesRequest struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

type scopeLogs struct {
	Scope      scope       `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type resourceLogs struct {
	Resource  resource    `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}

type logsRequest struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

func nanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

// SignalURL is where one signal ("traces", "logs") is posted: its own
// endpoint as given, else base with the signal's path added, as the
// OpenTelemetry environment variables are defined. "" when neither is set.
func SignalURL(base, own, signal string) string {
	if own != "" {
		return own
	}
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/v1/" + signal
}

// ParseHeaders reads OTEL_EXPORTER_OTLP_HEADERS: key=value pairs separated
// by commas, values percent-encoded.
func ParseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if k = strings.TrimSpace(k); !ok || k == "" {
			return nil, fmt.Errorf("header %q: want key=value", kv)
		}
		if dec, err := url.PathUnescape(strings.TrimSpace(v)); err == nil {
			v = dec
		}
		out[k] = v
	}
	return out, nil
}

// retryable are the answers OTLP/HTTP says to send again after.
func retryable(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// post sends body to url until the collector takes it. A collector that is
// down or answers with a retryable status is waited for, as long as it
// takes: nothing else is read from the yard meanwhile, so nothing is lost.
// Any other refusal is an error, since sending the same again cannot help
// (a wrong path, a missing token). ctx ending ends the wait with its error.
func (x *Exporter) post(ctx context.Context, url string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	wait := x.Backoff
	for try := 1; ; try++ {
		retryAfter, err := x.send(ctx, url, data)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, again := err.(retryErr); !again {
			return err
		}
		pause := wait
		if retryAfter > 0 {
			pause = retryAfter
		}
		pause = min(pause, x.MaxBackoff)
		x.Log.Warn("collector not reached; trying again", "url", url, "try", try, "in", pause, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
		wait = min(2*wait, x.MaxBackoff)
	}
}

// retryErr is a failed send worth repeating.
type retryErr struct{ error }

// send posts data once. retryAfter is what a Retry-After header asked for.
func (x *Exporter) send(ctx context.Context, url string, data []byte) (retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range x.Headers {
		req.Header.Set(k, v)
	}
	resp, err := x.Client.Do(req)
	if err != nil {
		return 0, retryErr{err}
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 == 2 {
		// A collector may take a request and still refuse some of it.
		var partial struct {
			PartialSuccess struct {
				RejectedSpans      json.Number `json:"rejectedSpans"`
				RejectedLogRecords json.Number `json:"rejectedLogRecords"`
				ErrorMessage       string      `json:"errorMessage"`
			} `json:"partialSuccess"`
		}
		if json.Unmarshal(answer, &partial) == nil {
			p := partial.PartialSuccess
			if (p.RejectedSpans != "" && p.RejectedSpans != "0") || (p.RejectedLogRecords != "" && p.RejectedLogRecords != "0") {
				x.Log.Warn("collector rejected part of a request", "url", url, "spans", p.RejectedSpans,
					"logs", p.RejectedLogRecords, "message", p.ErrorMessage)
			}
		}
		return 0, nil
	}
	err = fmt.Errorf("%s answered %s: %s", url, resp.Status, strings.TrimSpace(string(answer)))
	if !retryable(resp.StatusCode) {
		return 0, err
	}
	if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && s > 0 {
		retryAfter = time.Duration(s) * time.Second
	}
	return retryAfter, retryErr{err}
}
