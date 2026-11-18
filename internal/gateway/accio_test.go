package gateway

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

func TestAccioWorkGateway(t *testing.T) {
	for _, dialect := range []string{"contents", "messages", "helix", "helix-model-code", "helix-properties"} {
		t.Run(dialect, func(t *testing.T) {
			fresh(t)
			requests := make(chan []byte, 4)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				requests <- b
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("upstream %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				if !gjson.GetBytes(b, "stream").Bool() {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"call_example","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Shanghai\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\",\"tool_calls\":[{\"index\":0,\"id\":\"call_example\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
				io.WriteString(w, "data: {\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":4,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
			}))
			defer up.Close()
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: up.URL + "/v1", Key: "test-key", Models: []string{"m1", "m2"}}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(AccioConfigPath()), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(AccioConfigPath(), []byte(`{"model":"magpie/fake/m1"}`), 0600); err != nil {
				t.Fatal(err)
			}
			h := lanGuard(New().Handler())
			list := httptest.NewRecorder()
			r := httptest.NewRequest("GET", AccioPath+"/api/llm/config/v2", nil)
			r.RemoteAddr = "127.0.0.1:32100"
			h.ServeHTTP(list, r)
			if list.Code != 200 || !strings.Contains(list.Body.String(), `"modelCode":"fake/m1"`) || !strings.Contains(list.Body.String(), `"isDefault":true`) {
				t.Fatalf("model list: %d %s", list.Code, list.Body.String())
			}
			body := `{"model":"auto","system_instruction":{"parts":[{"text":"You are helpful"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"function_declarations":[{"name":"weather","parametersJson":"{\"type\":\"object\",\"properties\":{\"city\":{\"type\":\"string\"}}}"}]}],"properties":{"generationConfig":"{\"temperature\":0.2,\"maxOutputTokens\":64}"}}`
			if dialect == "messages" {
				body = `{"modelCode":"fake/m1","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`
			}
			wantModel := "m1"
			if strings.HasPrefix(dialect, "helix") {
				body = strings.Replace(body, `"model":"auto"`, `"model":"1Helix-3EjR3WN1dY6s"`, 1)
				if dialect == "helix-model-code" {
					body = strings.Replace(body, `"properties":{`, `"modelCode":"fake/m2","properties":{`, 1)
					wantModel = "m2"
				}
				if dialect == "helix-properties" {
					body = strings.Replace(body, `"properties":{`, `"properties":{"model_name":"fake/m2",`, 1)
					wantModel = "m2"
				}
			}
			// Accio's gzip-compressed SDK requests take the same path.
			var compressed bytes.Buffer
			gz := gzip.NewWriter(&compressed)
			gz.Write([]byte(body))
			gz.Close()
			r = httptest.NewRequest("POST", AccioPath+"/api/adk/llm/stream", &compressed)
			r.RemoteAddr = "127.0.0.1:32100"
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Content-Encoding", "gzip")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
				t.Fatalf("reply: %d %s", w.Code, w.Body.String())
			}
			var frame gjson.Result
			for _, line := range strings.Split(w.Body.String(), "\n") {
				if s, ok := strings.CutPrefix(line, "data: "); ok {
					frame = gjson.Parse(s)
				}
			}
			if frame.Get("errorCode").Exists() || !frame.Get("turnComplete").Bool() || frame.Get("partial").Bool() || frame.Get("content.role").String() != "model" {
				t.Fatalf("ADK event: %s", w.Body.String())
			}
			if !strings.Contains(frame.Get("content.parts").Raw, "hello") || !strings.Contains(frame.Get("content.parts").Raw, "Shanghai") || frame.Get("usageMetadata.promptTokenCount").Int() != 8 {
				t.Fatalf("text, tool arguments or usage lost: %s", frame.Raw)
			}
			select {
			case b := <-requests:
				if gjson.GetBytes(b, "model").String() != wantModel || !strings.Contains(string(b), "hi") || !strings.Contains(string(b), "weather") || !strings.Contains(string(b), "city") {
					t.Fatalf("upstream body: %s", b)
				}
			default:
				t.Fatal("no inference request reached the provider")
			}
		})
	}
}

// The installed Accio client uses postConditionalRaw with token and
// supportAutoModel in its body, rather than GET as early bridge docs say.
func TestAccioWorkModelCatalogMethods(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(AccioConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(AccioConfigPath(), []byte(`{"model":"magpie/fake/m1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.client.Transport = accioTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("model catalog must not forward the Accio token or call an upstream")
		return nil, nil
	})
	h := lanGuard(s.Handler())
	var getBody string
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		r := httptest.NewRequest(method, AccioPath+"/api/llm/config/v2", strings.NewReader(`{"token":"accio-test-session","supportAutoModel":true}`))
		r.RemoteAddr = "127.0.0.1:32100"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("x-language", "zh-CN")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if method == http.MethodPut {
			if w.Code != http.StatusNotImplemented {
				t.Fatalf("PUT: %d", w.Code)
			}
			continue
		}
		if w.Code != http.StatusOK || !gjson.Parse(w.Body.String()).IsArray() || gjson.Get(w.Body.String(), "0.modelList.0.modelCode").String() != "fake/m1" || !gjson.Get(w.Body.String(), "0.modelList.0.isDefault").Bool() {
			t.Fatalf("%s catalog: %d %s", method, w.Code, w.Body.String())
		}
		if method == http.MethodGet {
			getBody = w.Body.String()
		} else if w.Body.String() != getBody {
			t.Fatal("GET and POST catalogs differ")
		}
	}
}

func TestAccioWorkRequestAndFailures(t *testing.T) {
	flat := `{"model":"fake/m1","contents":[{"parts":[{"text":"hi"}]}],"tools":[{"name":"lookup","parameters":"{\"type\":\"object\",\"properties\":{\"record_id\":{\"type\":\"integer\",\"default\":9007199254740993}}}"}]}`
	fb, _, fe := accioRequest([]byte(flat), "")
	if fe != nil || gjson.GetBytes(fb, "tools.0.functionDeclarations.0.parameters.properties.record_id.default").Raw != "9007199254740993" {
		t.Fatalf("flat tool schema or exact integer lost: %s, %v", fb, fe)
	}
	chat := `{"model":"fake/m1","messages":[{"role":"assistant","tool_calls":[{"id":"call_one","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_one","content":"ok"}]}`
	cb, cp, ce := accioRequest([]byte(chat), "")
	if ce != nil || cp != provider.Chat || gjson.GetBytes(cb, "messages.1.tool_call_id").String() != "call_one" || !gjson.GetBytes(cb, "messages.0.tool_calls").IsArray() {
		t.Fatalf("Chat tool fields changed: %s, %v", cb, ce)
	}
	good := `{"modelName":"fake/m1","contents":[{"role":"model","parts":[{"functionCall":{"id":"call_one","name":"lookup","argsJson":"{\"q\":\"hello\"}"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call_one","name":"lookup","responseJson":"{\"result\":\"ok\"}"}},{"inline_data":{"mime_type":"image/png","data":"aGVsbG8="}}]}]}`
	b, p, err := accioRequest([]byte(good), "")
	if err != nil || p != provider.Gemini || gjson.GetBytes(b, "contents.0.parts.0.functionCall.args.q").String() != "hello" || gjson.GetBytes(b, "contents.1.parts.0.functionResponse.response.result").String() != "ok" || gjson.GetBytes(b, "contents.1.parts.1.inlineData.mimeType").String() != "image/png" {
		t.Fatalf("ADK tool/image normalization: %s, %s, %v", b, p, err)
	}
	for _, input := range []string{`null`, `[]`, `{}`, `{"model":"fake/m1"}`, `{"model":"auto","contents":[{"parts":[{"text":"hi"}]}]}`} {
		if _, _, err := accioRequest([]byte(input), ""); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	for _, input := range []string{`{}`, `{"candidates":[]}`, `{"error":{"message":"quota exhausted"}}`, `oops`} {
		if _, err := accioFrame([]byte(input), provider.Gemini); err == nil {
			t.Errorf("reported successful reply for %s", input)
		}
	}
}

func TestAccioWorkInternalModelSelection(t *testing.T) {
	fresh(t)
	const internal = "1Helix-3EjR3WN1dY6s"
	for _, tc := range []struct {
		name, fields, fallback, want string
	}{
		{"default", `"model":"` + internal + `"`, "fake/m1", "fake/m1"},
		{"no-default", `"model":"` + internal + `"`, "", ""},
		{"other-field", `"model":"` + internal + `","modelCode":"fake/m2"`, "fake/m1", "fake/m2"},
		{"properties", `"model":"` + internal + `","properties":{"model_name":"fake/m2"}`, "fake/m1", "fake/m2"},
		{"unknown-qualified", `"model":"missing/m2"`, "fake/m1", "missing/m2"},
		{"unknown-bare", `"model":"typo-model"`, "fake/m1", "typo-model"},
		{"explicit-helix", `"model":"fake/` + internal + `"`, "fake/m1", "fake/" + internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _, err := accioRequest([]byte(`{`+tc.fields+`,"messages":[{"role":"user","content":"hi"}]}`), tc.fallback)
			if tc.want == "" {
				if err == nil {
					t.Fatal("internal model without a configured default was accepted")
				}
				return
			}
			if err != nil || gjson.GetBytes(b, "model").String() != tc.want {
				t.Fatalf("want %q: %s, %v", tc.want, b, err)
			}
		})
	}
	// A provider may itself list a model with the same spelling.
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "http://127.0.0.1:1/v1", Models: []string{internal}}); err != nil {
		t.Fatal(err)
	}
	b, _, err := accioRequest([]byte(`{"model":"`+internal+`","messages":[{"role":"user","content":"hi"}]}`), "fake/m1")
	if err != nil || gjson.GetBytes(b, "model").String() != internal {
		t.Fatalf("registered model was replaced: %s, %v", b, err)
	}
}

func TestAccioWorkDoesNotFallbackToOfficial(t *testing.T) {
	fresh(t)
	h := lanGuard(New().Handler())
	for _, path := range []string{"/api/adk/llm", "/api/mcp/proxy/unsupported", "/api/tool/rlab/call", "/api/adk/embedding/embed"} {
		r := httptest.NewRequest("POST", AccioPath+path, strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}]}`))
		r.RemoteAddr = "127.0.0.1:32500"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Errorf("missing model/unsupported endpoint succeeded: %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, p := range []string{"/api/auth/login", "/api/team/list", "/api/llm/subAgent/routing"} {
		if !accioMetadataPath(p) {
			t.Errorf("metadata refused: %s", p)
		}
	}
	for _, p := range []string{"/api/adk/llm", "/api/mcp/proxy", "/api/auth/../adk/llm", "/api/auth/%2e%2e/adk/llm", "/api/auth//x", "//example.com/api/auth/login"} {
		if accioMetadataPath(p) {
			t.Errorf("unsafe metadata accepted: %s", p)
		}
	}
}

func TestAccioWorkMalformedConfigIsUnknown(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(AccioConfigPath()), 0700)
	os.WriteFile(AccioConfigPath(), []byte(`{"model":`), 0600)
	if m, err := AccioModel(); err == nil || m != "" {
		t.Fatalf("corrupt config: %q, %v", m, err)
	}
}

type accioTestTransport func(*http.Request) (*http.Response, error)

func TestAccioWorkWebSearchProxy(t *testing.T) {
	fresh(t)
	s := New()
	calls := 0
	status := 200
	s.client.Transport = accioTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.String() != "https://phoenix-gw.alibaba.com/api/web/search-proxy" {
			t.Fatalf("wrong search destination: %s %s", r.Method, r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(b, "accessToken").String() != "accio-session-test" || !strings.HasPrefix(gjson.GetBytes(b, "url").String(), "https://api.ydc-index.io/") {
			t.Fatalf("search request changed: %s", b)
		}
		if r.Header.Get("Cookie") != "accio=test" || r.Header.Get("Content-Encoding") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Package-Region") != "cn" {
			t.Fatalf("headers: %v", r.Header)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}, "Location": []string{"https://elsewhere.invalid"}}, Body: io.NopCloser(strings.NewReader(`{"code":"SUCCESS","data":{"organic_results":[{"title":"Example","link":"https://example.com"}]}}`)), Request: r}, nil
	})
	h := lanGuard(s.Handler())
	for _, endpoint := range []string{"https://api.ydc-index.io/v1/custom/s/search?q=test", "https://api.ydc-index.io/v1/contents"} {
		body := `{"url":"` + endpoint + `","method":"GET","accessToken":"accio-session-test","messageId":"test-id"}`
		if strings.HasSuffix(endpoint, "contents") {
			body = `{"url":"` + endpoint + `","method":"POST","body":"{\"urls\":[\"https://example.com\"],\"format\":\"markdown\"}","accessToken":"accio-session-test"}`
		}
		for _, code := range []int{200, 401, 429, 302} {
			status = code
			var compressed bytes.Buffer
			gz := gzip.NewWriter(&compressed)
			gz.Write([]byte(body))
			gz.Close()
			r := httptest.NewRequest("POST", AccioPath+"/api/web/search-proxy", &compressed)
			r.RemoteAddr = "127.0.0.1:32000"
			r.Header.Set("Content-Encoding", "gzip")
			r.Header.Set("Cookie", "accio=test")
			r.Header.Set("Authorization", "Bearer "+Token)
			r.Header.Set("X-Package-Region", "cn")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := code
			if code == 302 {
				want = 502
			}
			if w.Code != want || code == 200 && gjson.Get(w.Body.String(), "data.organic_results.0.title").String() != "Example" {
				t.Fatalf("search %d: %d %s", code, w.Code, w.Body.String())
			}
		}
	}
	if calls != 8 {
		t.Fatalf("unexpected upstream calls: %d", calls)
	}
	for _, path := range []string{"/api/web/search-proxy/other", "/api/web/other", "/api/mcp/proxy/unsupported", "/api/tool/tpp/call"} {
		r := httptest.NewRequest("POST", AccioPath+path, strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:32000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 501 || calls != 8 {
			t.Fatalf("unsupported endpoint forwarded: %s %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", AccioPath+"/api/web/search-proxy", nil)
	r.RemoteAddr = "127.0.0.1:32000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 501 || calls != 8 {
		t.Fatal("unsupported method forwarded")
	}
	r = httptest.NewRequest("POST", AccioPath+"/api/web/search-proxy", strings.NewReader(`{}`))
	r.RemoteAddr = "192.0.2.1:32000"
	w = httptest.NewRecorder()
	s.accioOfficial(w, r, "/api/web/search-proxy")
	if w.Code != 403 || calls != 8 {
		t.Fatal("remote caller reached official tools")
	}
}

func (f accioTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccioWorkMetadataBoundary(t *testing.T) {
	fresh(t)
	s := New()
	var calls int
	redirect := false
	s.client.Transport = accioTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "phoenix-gw.alibaba.com" || r.URL.Path != "/api/team/list" || r.Header.Get("Cookie") != "accio-session=test" {
			t.Errorf("metadata destination or cookie: %s, %v", r.URL, r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer accio-session" || r.Header.Get("X-Accio-Client") != "work" {
			t.Errorf("metadata authentication lost: %v", r.Header)
		}
		if redirect {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://elsewhere.invalid/"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
	})
	h := lanGuard(s.Handler())
	for _, redir := range []bool{false, true} {
		redirect = redir
		r := httptest.NewRequest("GET", AccioPath+"/api/team/list", nil)
		r.RemoteAddr = "127.0.0.1:32000"
		r.Header.Set("Cookie", "accio-session=test")
		r.Header.Set("Authorization", "Bearer accio-session")
		r.Header.Set("X-Accio-Client", "work")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 200
		if redir {
			want = 502
		}
		if w.Code != want {
			t.Fatalf("metadata redirect=%v: %d %s", redir, w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("metadata followed a redirect: %d calls", calls)
	}
	r := httptest.NewRequest("POST", AccioPath+"/api/adk/llm", strings.NewReader(`{"model":"missing/m1","contents":[{"parts":[{"text":"hi"}]}]}`))
	r.RemoteAddr = "127.0.0.1:32000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if calls != 2 || !strings.Contains(w.Body.String(), `"errorCode"`) || !strings.Contains(w.Body.String(), `"finishReason":"ERROR"`) {
		t.Fatalf("failed inference reached official or looked successful: calls=%d, %s", calls, w.Body.String())
	}
}
