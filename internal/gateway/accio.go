package gateway

// Accio Work's gateway dialect is documented by TF49/AI-Helper's
// src-tauri/src/accio/{bridge,protocol}.rs. Its contents are Gemini's,
// but its SSE events contain a content directly, without candidates.
// Inference goes through serveAgent, with magpie's routing and limits.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/provider"
)

const AccioPath = "/accio"

// AccioConfigPath is magpie's launch configuration, never Accio's own file.
func AccioConfigPath() string { return filepath.Join(appdir.Config(), "accio.json") }

// AccioModel reads the model picked in magpie. An unreadable configuration
// is unknown, so it must not silently select another, possibly paid model.
func AccioModel() (string, error) {
	b, err := os.ReadFile(AccioConfigPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var cfg struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("read Accio Work configuration: %w", err)
	}
	return strings.TrimPrefix(cfg.Model, "magpie/"), nil
}

// accio is registered on the ordinary mux: caller, read-only, browser,
// budget and concurrency guards still run before an Accio route does.
func (s *Server) accio(w http.ResponseWriter, r *http.Request) {
	r = r.Clone(context.WithValue(r.Context(), agentPathCtx{}, "accio"))
	p := strings.TrimPrefix(r.URL.Path, AccioPath)
	switch {
	// Accio's current model picker POSTs its token and supportAutoModel;
	// retain GET for older clients and local catalog checks.
	case p == "/api/llm/config/v2" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		s.accioModels(w, r)
	case (p == "/api/adk/llm" || strings.HasPrefix(p, "/api/adk/llm/")) && r.Method == http.MethodPost:
		s.accioLLM(w, r)
	case p == "/api/adk/embedding/embed" && r.Method == http.MethodPost:
		s.accioEmbedding(w, r)
	// Both web_search and web_fetch POST this envelope to Accio's
	// authenticated tool service. It is not a model inference fallback.
	case p == "/api/web/search-proxy" && r.Method == http.MethodPost:
		s.accioOfficial(w, r, p)
	case p == "/api/mcp/proxy" && r.Method == http.MethodPost:
		s.accioOfficial(w, r, p)
	case accioMetadataPath(p):
		s.accioOfficial(w, r, p)
	default:
		writeError(w, provider.Chat, http.StatusNotImplemented, "this Accio Work endpoint is not supported by magpie; no request was sent to Accio's inference service")
	}
}

func (s *Server) accioModels(w http.ResponseWriter, r *http.Request) {
	selected, err := AccioModel()
	if err != nil {
		writeError(w, provider.Chat, 500, err.Error())
		return
	}
	models := []map[string]any{}
	for _, e := range catalogFor(r) {
		m := map[string]any{
			"modelCode": e.ID, "modelName": e.ID, "modelDisplayName": e.Name,
			"modelDesc": e.Name + " via magpie", "visible": true,
			"isDefault": e.ID == selected, "freeUse": true, "multimodal": e.Images,
		}
		if e.Context > 0 {
			m["contextWindow"] = e.Context
		}
		if len(e.Efforts) > 0 {
			m["reasoningEfforts"] = e.Efforts
		}
		models = append(models, m)
	}
	writeJSON(w, 200, []map[string]any{{"provider": "magpie", "providerDisplayName": "magpie", "modelList": models}})
}

func (s *Server) accioLLM(w http.ResponseWriter, r *http.Request) {
	body, ok := s.requestBody(w, r, provider.Gemini)
	if !ok {
		return
	}
	fallback, err := AccioModel()
	if err != nil {
		writeError(w, provider.Chat, 500, err.Error())
		return
	}
	body, from, err := accioRequest(body, fallback)
	if err != nil {
		writeError(w, provider.Chat, 400, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.Clone(ctx)
	r.Header.Del("Content-Encoding")
	r.Header.Del("Content-Length")
	r.Header.Del("Accept-Encoding")
	// The reference bridge returns one completed ADK event. Preserve that
	// contract, with comment heartbeats while the model is working. Only the
	// heartbeat goroutine writes w until it has been stopped and joined.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if _, err := io.WriteString(w, ": magpie connected\n\n"); err != nil {
		return
	}
	flush := func() { _ = http.NewResponseController(w).Flush() }
	flush()
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := io.WriteString(w, ": magpie heartbeat\n\n"); err != nil {
					cancel()
					return
				}
				flush()
			}
		}
	}()
	capture := &accioReply{header: http.Header{}}
	s.serveAgent(capture, r, from, body)
	close(stop)
	<-stopped
	frame, err := accioFrame(capture.body.Bytes(), from)
	if capture.failed || capture.status >= 300 || err != nil {
		message := "Accio Work model request failed"
		if e := gjson.GetBytes(capture.body.Bytes(), "error.message").String(); e != "" {
			message = e
		}
		if capture.failed {
			message = "Accio Work model reply exceeded 32 MiB"
		}
		if err != nil && capture.status < 300 {
			message = err.Error()
		}
		status := capture.status
		if status < 300 {
			status = 502
		}
		frame = map[string]any{"errorCode": fmt.Sprint(status), "errorMessage": message,
			"content":      map[string]any{"role": "model", "parts": []map[string]any{{"text": message}}},
			"finishReason": "ERROR", "turnComplete": true, "partial": false}
	}
	b, _ := json.Marshal(frame)
	fmt.Fprintf(w, "data: %s\n\n", b)
	flush()
}

// accioReply bounds the whole reply while the gateway gathers a stream.
type accioReply struct {
	header http.Header
	status int
	body   bytes.Buffer
	failed bool
}

func (a *accioReply) Header() http.Header { return a.header }
func (a *accioReply) WriteHeader(n int) {
	if a.status == 0 {
		a.status = n
	}
}
func (a *accioReply) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.WriteHeader(200)
	}
	if a.body.Len()+len(b) > 32<<20 {
		a.failed = true
		return 0, errors.New("Accio reply too large")
	}
	return a.body.Write(b)
}
func (a *accioReply) Flush() {}

func accioRequest(body []byte, fallback string) ([]byte, provider.Protocol, error) {
	var v map[string]any
	// Chat's tool_calls and tool_call_id must retain their own spelling.
	if gjson.GetBytes(body, "contents").IsArray() {
		body = geminiCamel(body)
	}
	if err := accioDecode(body, &v); err != nil || v == nil {
		return nil, "", errors.New("Accio Work request must be a JSON object")
	}
	model := ""
	for _, obj := range []map[string]any{v, accioObject(v["properties"])} {
		for _, key := range []string{"model", "modelCode", "model_code", "modelName", "model_name"} {
			if s, ok := obj[key].(string); ok && strings.TrimSpace(s) != "" && !strings.EqualFold(strings.TrimSpace(s), "auto") {
				s = strings.TrimSpace(s)
				// Accio can retain its internal Helix code in a conversation
				// even with ADK_MODEL set. Look for another explicit field,
				// then use the user's configured model, never an arbitrary one.
				// Only this observed internal namespace gets that treatment;
				// typos and explicit provider/model references still fail normally.
				if strings.HasPrefix(s, "1Helix-") && len(s) > len("1Helix-") && !strings.Contains(s, "/") {
					if _, _, known := provider.Resolve(s); !known {
						continue
					}
				}
				model = s
				break
			}
		}
		if model != "" {
			break
		}
	}
	if model == "" {
		model = fallback
	}
	if model == "" {
		return nil, "", errors.New("choose an Accio Work model in magpie first")
	}
	v["model"], v["stream"] = strings.TrimPrefix(model, "magpie/"), false
	from := provider.Gemini
	if contents, ok := v["contents"].([]any); !ok || len(contents) == 0 {
		if messages, ok := v["messages"].([]any); !ok || len(messages) == 0 {
			return nil, "", errors.New("Accio Work request needs contents or messages")
		}
		from = provider.Chat
	} else {
		if v["generationConfig"] == nil {
			g := accioObject(v["properties"])["generationConfig"]
			if s, ok := g.(string); ok {
				var parsed any
				if accioDecode([]byte(s), &parsed) != nil {
					return nil, "", errors.New("invalid generationConfig JSON")
				}
				g = parsed
			}
			if g != nil {
				v["generationConfig"] = g
			}
		}
		if err := accioNormalize(v); err != nil {
			return nil, "", err
		}
	}
	out, err := json.Marshal(v)
	return out, from, err
}

func accioObject(v any) map[string]any { m, _ := v.(map[string]any); return m }

func accioDecode(b []byte, v any) error {
	if !json.Valid(b) {
		return errors.New("invalid JSON")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

// ADK sometimes serializes schemas, arguments and results as JSON strings.
// Normalize only protocol fields, never the contents of a user's schema.
func accioNormalize(v map[string]any) error {
	for _, c := range accioArray(v["contents"]) {
		for _, p := range accioArray(accioObject(c)["parts"]) {
			part := accioObject(p)
			for _, key := range []string{"functionCall", "functionResponse"} {
				f := accioObject(part[key])
				target, source := "args", "argsJson"
				if key == "functionResponse" {
					target, source = "response", "responseJson"
				}
				if f[target] == nil {
					if s, ok := f[source].(string); ok {
						var x any
						if err := accioDecode([]byte(s), &x); err != nil {
							return fmt.Errorf("invalid %s: %w", source, err)
						}
						f[target] = x
					}
				}
			}
		}
	}
	tools := accioArray(v["tools"])
	for i, t := range tools {
		tool := accioObject(t)
		if tool["name"] != nil {
			tool = map[string]any{"functionDeclarations": []any{tool}}
			tools[i] = tool
		}
		for _, d := range accioArray(tool["functionDeclarations"]) {
			f := accioObject(d)
			parameters := f["parameters"]
			if parameters == nil {
				parameters = f["parametersJson"]
			}
			if s, ok := parameters.(string); ok {
				if err := accioDecode([]byte(s), &parameters); err != nil {
					return fmt.Errorf("invalid tool parameters JSON: %w", err)
				}
			}
			if parameters != nil {
				if accioObject(parameters) == nil {
					return errors.New("tool parameters must be a JSON object")
				}
				f["parameters"] = parameters
			}
		}
	}
	return nil
}
func accioArray(v any) []any { a, _ := v.([]any); return a }

func accioFrame(body []byte, from provider.Protocol) (map[string]any, error) {
	if !json.Valid(body) {
		return nil, errors.New("model returned an invalid JSON reply")
	}
	root := gjson.ParseBytes(body)
	if root.Get("error").Exists() {
		return nil, errors.New(root.Get("error.message").String())
	}
	var content any
	var usage any
	finish := "STOP"
	model := root.Get("modelVersion").String()
	if from == provider.Gemini {
		candidate := root.Get("candidates.0")
		if !candidate.Get("content.parts").IsArray() {
			return nil, errors.New("model reply has no content")
		}
		if err := accioDecode([]byte(candidate.Get("content").Raw), &content); err != nil {
			return nil, err
		}
		usage = root.Get("usageMetadata").Value()
		for _, p := range accioArray(accioObject(content)["parts"]) {
			if call := accioObject(accioObject(p)["functionCall"]); call != nil {
				args, _ := json.Marshal(call["args"])
				call["argsJson"] = string(args)
			}
		}
		if s := candidate.Get("finishReason").String(); s != "" {
			finish = s
		}
	} else {
		choice := root.Get("choices.0")
		if !choice.Exists() {
			return nil, errors.New("model reply has no choices")
		}
		parts := []map[string]any{}
		if text := choice.Get("message.content").String(); text != "" {
			parts = append(parts, map[string]any{"text": text})
		}
		for _, call := range choice.Get("message.tool_calls").Array() {
			args := call.Get("function.arguments").String()
			if !json.Valid([]byte(args)) {
				return nil, errors.New("model returned invalid tool arguments")
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"id": call.Get("id").String(), "name": call.Get("function.name").String(), "args": json.RawMessage(args), "argsJson": args}})
		}
		content = map[string]any{"role": "model", "parts": parts}
		usage = map[string]any{"promptTokenCount": root.Get("usage.prompt_tokens").Int(), "candidatesTokenCount": root.Get("usage.completion_tokens").Int(), "totalTokenCount": root.Get("usage.total_tokens").Int()}
		if choice.Get("finish_reason").String() == "length" {
			finish = "MAX_TOKENS"
		}
		model = root.Get("model").String()
	}
	return map[string]any{"content": content, "usageMetadata": usage, "finishReason": finish, "customMetadata": map[string]any{"model_name": model, "bridge": "magpie"}, "turnComplete": true, "partial": false}, nil
}

func (s *Server) accioEmbedding(w http.ResponseWriter, r *http.Request) {
	body, ok := s.requestBody(w, r, provider.Chat)
	if !ok {
		return
	}
	var v map[string]any
	if json.Unmarshal(body, &v) != nil || v == nil {
		writeError(w, provider.Chat, 400, "invalid embedding request")
		return
	}
	if v["input"] == nil {
		v["input"] = v["texts"]
	}
	delete(v, "texts")
	// An explicit provider/model is required; never pick a paid embedding
	// model implicitly. retrieve applies the ordinary key and model limits.
	b, _ := json.Marshal(v)
	in := r.Clone(r.Context())
	in.Body, in.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
	in.Header.Del("Content-Encoding")
	in.Header.Del("Content-Length")
	s.retrieve("/embeddings", "embeddings")(w, in)
}

// This is the metadata allow list. The one supported web-tool route is
// matched separately above. Unknown, inference and image paths fail closed.
func accioMetadataPath(p string) bool {
	for _, c := range p {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/-_", c)) {
			return false
		}
	}
	if strings.Contains(p, "//") {
		return false
	}
	for _, exact := range []string{"/api/media/catalog", "/api/video/models", "/api/llm/subAgent/routing"} {
		if p == exact {
			return true
		}
	}
	for _, prefix := range []string{"/api/auth/", "/api/meta/sync/", "/api/tool/featureFlag/", "/api/team/", "/api/agent-team/", "/api/agent-hub/", "/api/invitation/", "/api/channel/", "/api/skill/", "/api/plugin/", "/api/onboarding/", "/api/heartbeat/"} {
		if strings.HasPrefix(p, prefix) && len(p) > len(prefix) {
			return true
		}
	}
	return false
}

func (s *Server) accioOfficial(w http.ResponseWriter, r *http.Request, p string) {
	if !local(r) {
		writeError(w, provider.Chat, 403, "Accio Work official tools and metadata are available on this computer only")
		return
	}
	body, ok := s.requestBody(w, r, provider.Chat)
	if !ok {
		return
	}
	timeout := 30 * time.Second
	if p == "/api/mcp/proxy" {
		// The installed SDK sends an authenticated JSON-RPC envelope.
		// Refuse malformed or unauthenticated envelopes locally.
		v := gjson.ParseBytes(body)
		if !json.Valid(body) || !v.IsObject() || v.Get("accessToken").Type != gjson.String || strings.TrimSpace(v.Get("accessToken").String()) == "" || !v.Get("request").IsObject() || v.Get("request.method").Type != gjson.String || strings.TrimSpace(v.Get("request.method").String()) == "" {
			writeError(w, provider.Chat, 400, "Accio Work MCP requires an accessToken and a JSON-RPC request with a method")
			return
		}
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	u := "https://phoenix-gw.alibaba.com" + p
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	in, err := http.NewRequestWithContext(ctx, r.Method, u, bytes.NewReader(body))
	if err != nil {
		writeError(w, provider.Chat, 400, err.Error())
		return
	}
	for _, key := range []string{"Authorization", "Cookie", "Content-Type", "User-Agent", "Accept", "Accept-Language", "X-App-Version", "X-Package-Region", "X-Language", "X-Source"} {
		if v := r.Header.Get(key); v != "" {
			in.Header.Set(key, v)
		}
	}
	// Accio's business headers accompany its login cookies. Gateway keys
	// and magpie's own headers must not become credentials at that origin.
	for key, values := range r.Header {
		k := strings.ToLower(key)
		if strings.HasPrefix(k, "x-accio-") || strings.HasPrefix(k, "x-client-") {
			in.Header[key] = append([]string(nil), values...)
		}
	}
	if strings.HasPrefix(in.Header.Get("Authorization"), "Bearer "+Token) {
		in.Header.Del("Authorization")
	}
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(in)
	if err != nil {
		writeError(w, provider.Chat, 502, "Accio Work official service request failed")
		return
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		writeError(w, provider.Chat, 502, "Accio Work official service redirect refused")
		return
	}
	for _, key := range []string{"Content-Type", "Set-Cookie", "Retry-After"} {
		for _, v := range res.Header.Values(key) {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	var out io.Writer = w
	if strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
		out = accioEventWriter{w}
	}
	io.Copy(out, io.LimitReader(res.Body, 16<<20))
}

// Flush each upstream SSE chunk rather than buffering a long-running
// business tool until it completes. There is no inference retry/fallback.
type accioEventWriter struct{ http.ResponseWriter }

func (w accioEventWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if err == nil {
		err = http.NewResponseController(w.ResponseWriter).Flush()
		if errors.Is(err, http.ErrNotSupported) {
			err = nil
		}
	}
	return n, err
}
