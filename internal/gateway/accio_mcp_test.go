package gateway

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The installed SDK POSTs an accessToken and a nested JSON-RPC request;
// it accepts either a JSON envelope or an SSE data event in reply.
func TestAccioWorkMCPProxy(t *testing.T) {
	fresh(t)
	s := New()
	const body = `{"accessToken":"accio-session-test","request":{"jsonrpc":"2.0","id":17,"method":"tools/list","params":{}},"requestId":"test-request"}`
	calls := 0
	for _, tc := range []struct {
		status             int
		contentType, reply string
		broken             bool
	}{
		{200, "application/json", `{"success":true,"data":{"jsonrpc":"2.0","id":17,"result":{"tools":[]}}}`, false},
		{200, "text/event-stream", "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":17,\"result\":{\"tools\":[]}}\n\n", false},
		{200, "application/json", `{"jsonrpc":"2.0","id":17,"error":{"code":-32002,"message":"tool gateway unavailable"}}`, false},
		{401, "application/json", `{"message":"login expired"}`, false},
		{429, "application/json", `{"message":"rate limited"}`, false},
		{302, "application/json", `{}`, false},
		{502, "application/json", `{}`, true},
	} {
		s.client.Transport = accioTestTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "POST" || r.URL.String() != "https://phoenix-gw.alibaba.com/api/mcp/proxy" {
				t.Errorf("destination: %s %s", r.Method, r.URL)
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != body || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Encoding") != "" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				t.Errorf("MCP request changed")
			}
			if tc.broken {
				return nil, errors.New("upstream unavailable")
			}
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{tc.contentType}, "Location": []string{"https://elsewhere.invalid/"}, "Retry-After": []string{"10"}}, Body: io.NopCloser(strings.NewReader(tc.reply)), Request: r}, nil
		})
		var gz bytes.Buffer
		z := gzip.NewWriter(&gz)
		z.Write([]byte(body))
		z.Close()
		r := httptest.NewRequest("POST", AccioPath+"/api/mcp/proxy", &gz)
		r.RemoteAddr = "127.0.0.1:32000"
		r.Header.Set("Content-Encoding", "gzip")
		r.Header.Set("Authorization", "Bearer "+Token)
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		lanGuard(s.Handler()).ServeHTTP(w, r)
		want := tc.status
		if want == 302 {
			want = 502
		}
		if w.Code != want {
			t.Fatalf("MCP %d: %d %s", tc.status, w.Code, w.Body.String())
		}
		if !tc.broken && tc.status < 300 && (w.Body.String() != tc.reply || w.Header().Get("Content-Type") != tc.contentType) {
			t.Fatalf("MCP reply changed: %s", w.Body.String())
		}
		if tc.status == 429 && w.Header().Get("Retry-After") != "10" {
			t.Fatal("retry-after lost")
		}
	}
	before := calls
	for _, invalid := range []string{`{}`, `null`, `{"accessToken":"x","request":null}`, `{"accessToken":"x","request":{"method":""}}`} {
		r := httptest.NewRequest("POST", AccioPath+"/api/mcp/proxy", strings.NewReader(invalid))
		r.RemoteAddr = "127.0.0.1:32000"
		w := httptest.NewRecorder()
		lanGuard(s.Handler()).ServeHTTP(w, r)
		if w.Code != 400 || calls != before {
			t.Fatalf("invalid request forwarded: %s %d", invalid, w.Code)
		}
	}
}

type accioMCPStream struct {
	first  bool
	finish <-chan struct{}
}

func (b *accioMCPStream) Read(p []byte) (int, error) {
	if !b.first {
		b.first = true
		return copy(p, "data: {\"jsonrpc\":\"2.0\",\"id\":17}\n\n"), nil
	}
	<-b.finish
	return 0, io.EOF
}
func (*accioMCPStream) Close() error { return nil }

type accioFlushWatch struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
	once    sync.Once
}

func (w *accioFlushWatch) Flush() { w.ResponseRecorder.Flush(); w.once.Do(func() { close(w.flushed) }) }

func TestAccioWorkMCPFlushesBeforeCompletion(t *testing.T) {
	fresh(t)
	s := New()
	finish := make(chan struct{})
	done := make(chan struct{})
	s.client.Transport = accioTestTransport(func(r *http.Request) (*http.Response, error) {
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) < time.Minute {
			t.Error("MCP timeout shorter than a minute")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &accioMCPStream{finish: finish}, Request: r}, nil
	})
	w := &accioFlushWatch{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{})}
	r := httptest.NewRequest("POST", AccioPath+"/api/mcp/proxy", strings.NewReader(`{"accessToken":"test","request":{"jsonrpc":"2.0","id":17,"method":"tools/list"}}`))
	r.RemoteAddr = "127.0.0.1:32000"
	t.Cleanup(func() { close(finish); <-done })
	go func() { defer close(done); lanGuard(s.Handler()).ServeHTTP(w, r) }()
	select {
	case <-w.flushed:
	case <-done:
		t.Fatal("MCP request did not start streaming")
	case <-time.After(5 * time.Second):
		t.Fatal("MCP buffered the event until completion")
	}
}
