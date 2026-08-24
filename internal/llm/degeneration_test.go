package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// incidentMSG1604Tail is the tail of the 2026-08-22 butler incident reply
// (MSG 1604, Discord): qwen3.6-35b-a3b leaked its thinking into content,
// degenerated into an `I will propose the "X".` loop, and hit the 4096-token
// cap (finish_reason=length). Source: data/fc-butler-degeneration-diag,
// step1-db.txt L706-758.
const incidentMSG1604Tail = `I will make sure the tone is "expert engineer".
I will not be chatty.
I will focus on the *optimal* solution.
The optimal solution for 80ft is **Zoning**.
I will emphasize that.
I will also mention **Pressure Regulators** per zone.
This is getting complex but that's what they asked for.
"Optimal solution".
Okay.
I'll write it now.
I will not mention "I can't search".
I will just give the answer.
The answer is: Zoning + Pump + PTZ Camera.
Done.
I will check the "servo + sunbattarer" part.
User has solar.
Pump needs power.
12V pump draws ~2-4A.
Solar needs to be bigger or battery needs to be bigger.
I will update the solar math.
20W panel -> 4A peak.
Pump running = 4A.
Battery drains fast.
Solution: Pump only runs when sun is out? Or larger panel?
Recommend **40W Panel**.
Or run pump only for 10 mins max.
I'll suggest **40W Panel**.
It's safer.
Okay.
Ready.
I will write the response.
I will use the "architect" persona.
I will address the "80ft" constraint directly.
I will propose the "Linear Manifold".
I will propose the "PTZ Camera".
I will propose the "VL Model Scan".
I will propose the "Pump".
I will propose the "Zoning".
I will propose the "Drip Option".
I will propose the "3D Print parts".
I will propose the "STM32 Logic".
I will propose the "Mac Mini Logic".
I will propose the "BOM".
I will propose the "Build Order".
I will propose the "Risks".
I will propose the "Mitigation".
I will propose the "Future Upgrades".
I will propose the "Cost".
I will propose the "Time".
I will propose the "Difficulty".
I will propose the "Fun Factor".
I will propose the "Wow Factor".
I will propose the "Reliability".`

// cleanProse returns n unique words: a long response with no repeated
// 4-gram, mirroring the report's "clean answers ALSO hit 4096" repro — the
// guard must not fire on this.
func cleanProse(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("uniqueword%04d", i))
	}
	return strings.Join(parts, " ")
}

func TestHasDegenerationLoop(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "incident MSG 1604 tail", text: incidentMSG1604Tail, want: true},
		{name: "pure word loop", text: strings.Repeat("done ", 45), want: true},
		{name: "clean long answer truncated at cap", text: cleanProse(300), want: false},
		{name: "phrase repeated under threshold", text: strings.Repeat("in order to check ", 4) + cleanProse(40), want: false},
		{name: "short text with heavy repetition", text: strings.Repeat("loop loop ", 10), want: false},
		{name: "empty", text: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDegenerationLoop(tt.text); got != tt.want {
				t.Errorf("hasDegenerationLoop() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDegenerationRetryParams(t *testing.T) {
	tests := []struct {
		name          string
		temp          float64
		maxTokens     int
		wantTemp      float64
		wantMaxTokens int
	}{
		{name: "typical", temp: 0.7, maxTokens: 4096, wantTemp: 0.35, wantMaxTokens: 2048},
		{name: "already mild", temp: 0.3, maxTokens: 4096, wantTemp: 0.15, wantMaxTokens: 2048},
		{name: "floor at 0.1", temp: 0.14, maxTokens: 4096, wantTemp: 0.1, wantMaxTokens: 2048},
		{name: "greedy stays greedy, cap still halved", temp: 0, maxTokens: 4096, wantTemp: 0, wantMaxTokens: 2048},
		{name: "small cap kept", temp: 0.7, maxTokens: 256, wantTemp: 0.35, wantMaxTokens: 256},
		{name: "tiny cap kept", temp: 0.7, maxTokens: 200, wantTemp: 0.35, wantMaxTokens: 200},
		{name: "no cap stays no cap", temp: 0.7, maxTokens: 0, wantTemp: 0.35, wantMaxTokens: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTemp, gotCap := degenerationRetryParams(tt.temp, tt.maxTokens)
			if math.Abs(gotTemp-tt.wantTemp) > 1e-9 {
				t.Errorf("temp = %v, want %v", gotTemp, tt.wantTemp)
			}
			if gotCap != tt.wantMaxTokens {
				t.Errorf("cap = %d, want %d", gotCap, tt.wantMaxTokens)
			}
		})
	}
}

// queuedLLMServer replays queued JSON responses against /chat/completions
// and records every request received, so the guard tests can assert how many
// attempts were made and with what sampling parameters.
type queuedLLMServer struct {
	*httptest.Server
	mu    sync.Mutex
	resps []queuedResponse
	reqs  []openaiRequest
}

type queuedResponse struct {
	status int
	body   string
}

func newQueuedLLMServer(responses ...queuedResponse) *queuedLLMServer {
	q := &queuedLLMServer{resps: responses}
	q.Server = httptest.NewServer(http.HandlerFunc(q.handle))
	return q
}

func (q *queuedLLMServer) handle(w http.ResponseWriter, r *http.Request) {
	var req openaiRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	q.mu.Lock()
	if len(q.resps) == 0 {
		q.mu.Unlock()
		http.Error(w, "out of queued responses", http.StatusInternalServerError)
		return
	}
	resp := q.resps[0]
	q.resps = q.resps[1:]
	q.reqs = append(q.reqs, req)
	q.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.status)
	_, _ = io.WriteString(w, resp.body)
}

func (q *queuedLLMServer) requestCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.reqs)
}

func (q *queuedLLMServer) request(i int) openaiRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.reqs[i]
}

// fakeResponseBody renders a non-streaming chat completion response.
func fakeResponseBody(msg Message, finish string) string {
	var out struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	out.Choices = []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	}{
		{Message: msg, FinishReason: finish},
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// fakeSSEBody renders a two-chunk streaming response: one content delta, a
// finish chunk, then [DONE].
func fakeSSEBody(content, finish string) string {
	contentChunk, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]any{"content": content}}},
	})
	finishChunk, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": finish}},
	})
	return fmt.Sprintf("data: %s\ndata: %s\ndata: [DONE]\n", contentChunk, finishChunk)
}

// assertRequestShape checks the recorded request count, first-request
// parameters, and (when wantRetry is set) the retry's parameters.
func assertRequestShape(t *testing.T, srv *queuedLLMServer, wantReqs int, wantRetryTemp float64, wantRetryCap int, expectRetry bool) {
	t.Helper()
	if got := srv.requestCount(); got != wantReqs {
		t.Fatalf("requests = %d, want %d", got, wantReqs)
	}
	first := srv.request(0)
	if math.Abs(first.Temperature-0.7) > 1e-9 || first.MaxTokens != 4096 {
		t.Errorf("first request temp=%v max=%d, want 0.7/4096", first.Temperature, first.MaxTokens)
	}
	if !expectRetry {
		return
	}
	second := srv.request(1)
	if math.Abs(second.Temperature-wantRetryTemp) > 1e-9 || second.MaxTokens != wantRetryCap {
		t.Errorf("retry temp=%v max=%d, want %v/%d", second.Temperature, second.MaxTokens, wantRetryTemp, wantRetryCap)
	}
}

func TestChatMessageDegenerationGuard(t *testing.T) {
	ctx := context.Background()
	msgs := []Message{{Role: "user", Content: "How do I irrigate an 80ft flower bed along the fence?"}}
	tools := []ToolDef{{Type: "function", Function: ToolDefFunc{Name: "web_search", Description: "search the web", Parameters: map[string]any{}}}}
	loopToolCall := ToolCall{
		ID:       "call_1",
		Type:     "function",
		Function: ToolCallFunction{Name: "web_search", Arguments: ToolCallArguments{"query": "80ft irrigation"}},
	}

	tests := []struct {
		name          string
		withTools     bool
		respToolCalls int
		responses     []queuedResponse
		wantOut       string
		wantReqs      int
		wantRetry     bool
	}{
		{
			name:      "clean answer truncated at cap is not retried",
			responses: []queuedResponse{{status: http.StatusOK, body: fakeResponseBody(Message{Role: "assistant", Content: cleanProse(300)}, "length")}},
			wantOut:   cleanProse(300),
			wantReqs:  1,
		},
		{
			name: "incident shape retried once with adjusted sampling",
			responses: []queuedResponse{
				{status: http.StatusOK, body: fakeResponseBody(Message{Role: "assistant", Content: incidentMSG1604Tail}, "length")},
				{status: http.StatusOK, body: fakeResponseBody(Message{Role: "assistant", Content: "Concise answer: zone the 80ft bed into four 20ft zones."}, "stop")},
			},
			wantOut:   "Concise answer: zone the 80ft bed into four 20ft zones.",
			wantReqs:  2,
			wantRetry: true,
		},
		{
			name: "retry still degenerating delivers fail-soft message",
			responses: []queuedResponse{
				{status: http.StatusOK, body: fakeResponseBody(Message{Role: "assistant", Content: incidentMSG1604Tail}, "length")},
				{status: http.StatusOK, body: fakeResponseBody(Message{Role: "assistant", Content: incidentMSG1604Tail}, "length")},
			},
			wantOut:   DegenerationFallback,
			wantReqs:  2,
			wantRetry: true,
		},
		{
			name: "retry error delivers fail-soft message",
			responses: []queuedResponse{
				{status: http.StatusOK, body: fakeResponseBody(Message{Role: "assistant", Content: incidentMSG1604Tail}, "length")},
				{status: http.StatusBadGateway, body: "boom"},
			},
			wantOut:   DegenerationFallback,
			wantReqs:  2,
			wantRetry: true,
		},
		{
			name:      "loop shape but finish stop is not retried",
			responses: []queuedResponse{{status: http.StatusOK, body: fakeResponseBody(Message{Role: "assistant", Content: incidentMSG1604Tail}, "stop")}},
			wantOut:   incidentMSG1604Tail,
			wantReqs:  1,
		},
		{
			name:          "truncated tool call is not the guard's business",
			withTools:     true,
			responses:     []queuedResponse{{status: http.StatusOK, body: fakeResponseBody(Message{Role: "assistant", Content: incidentMSG1604Tail, ToolCalls: []ToolCall{loopToolCall}}, "length")}},
			wantOut:       incidentMSG1604Tail,
			wantReqs:      1,
			respToolCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newQueuedLLMServer(tt.responses...)
			defer srv.Close()
			client := NewClient(srv.URL+"/v1", "qwen3.6-35b-a3b", "")

			var msg *Message
			var err error
			if tt.withTools {
				msg, err = client.ChatWithTools(ctx, msgs, 0.7, 4096, tools)
			} else {
				msg, err = client.ChatMessage(ctx, msgs, 0.7, 4096)
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if msg.Content != tt.wantOut {
				t.Errorf("content = %q, want %q", msg.Content, tt.wantOut)
			}
			if len(msg.ToolCalls) != tt.respToolCalls {
				t.Errorf("tool calls = %d, want %d", len(msg.ToolCalls), tt.respToolCalls)
			}
			assertRequestShape(t, srv, tt.wantReqs, 0.35, 2048, tt.wantRetry)
		})
	}
}

func TestChatDegenerationGuardStreaming(t *testing.T) {
	ctx := context.Background()
	msgs := []Message{{Role: "user", Content: "How do I irrigate an 80ft flower bed along the fence?"}}

	tests := []struct {
		name      string
		responses []queuedResponse
		wantOut   string
		wantBuf   string // expected joined onToken chunks; empty = not checked
		wantReqs  int
		wantRetry bool
	}{
		{
			name:      "clean stream truncated at cap is not retried",
			responses: []queuedResponse{{status: http.StatusOK, body: fakeSSEBody(cleanProse(300), "length")}},
			wantOut:   cleanProse(300),
			wantReqs:  1,
		},
		{
			name: "incident shape retried once with adjusted sampling",
			responses: []queuedResponse{
				{status: http.StatusOK, body: fakeSSEBody(incidentMSG1604Tail, "length")},
				{status: http.StatusOK, body: fakeSSEBody("Concise answer: zone the 80ft bed.", "stop")},
			},
			wantOut:   "Concise answer: zone the 80ft bed.",
			wantReqs:  2,
			wantRetry: true,
		},
		{
			name: "retry still degenerating delivers fail-soft message",
			responses: []queuedResponse{
				{status: http.StatusOK, body: fakeSSEBody(incidentMSG1604Tail, "length")},
				{status: http.StatusOK, body: fakeSSEBody(incidentMSG1604Tail, "length")},
			},
			wantOut:   DegenerationFallback,
			wantReqs:  2,
			wantRetry: true,
		},
		{
			name: "retry error delivers fail-soft message",
			responses: []queuedResponse{
				{status: http.StatusOK, body: fakeSSEBody(incidentMSG1604Tail, "length")},
				{status: http.StatusBadGateway, body: "boom"},
			},
			wantOut:   DegenerationFallback,
			wantReqs:  2,
			wantRetry: true,
		},
		{
			name:      "loop shape but finish stop is not retried",
			responses: []queuedResponse{{status: http.StatusOK, body: fakeSSEBody(incidentMSG1604Tail, "stop")}},
			wantOut:   incidentMSG1604Tail,
			wantReqs:  1,
		},
		{
			// Documents the retry contract for callers that buffer onToken
			// chunks (agent.go): the joined buffer differs from the returned
			// text, so the drain logic emits the returned text verbatim and
			// the degenerate first attempt never reaches a gateway.
			name: "retry chunks append to the caller's token buffer",
			responses: []queuedResponse{
				{status: http.StatusOK, body: fakeSSEBody(incidentMSG1604Tail, "length")},
				{status: http.StatusOK, body: fakeSSEBody("Concise answer: zone the 80ft bed.", "stop")},
			},
			wantOut:   "Concise answer: zone the 80ft bed.",
			wantBuf:   incidentMSG1604Tail + "Concise answer: zone the 80ft bed.",
			wantReqs:  2,
			wantRetry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newQueuedLLMServer(tt.responses...)
			defer srv.Close()
			client := NewClient(srv.URL+"/v1", "qwen3.6-35b-a3b", "")

			var buf []string
			out, err := client.Chat(ctx, msgs, 0.7, 4096, func(tok string) { buf = append(buf, tok) })
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out != tt.wantOut {
				t.Errorf("content = %q, want %q", out, tt.wantOut)
			}
			if tt.wantBuf != "" && strings.Join(buf, "") != tt.wantBuf {
				t.Errorf("token buffer = %q, want %q", strings.Join(buf, ""), tt.wantBuf)
			}
			assertRequestShape(t, srv, tt.wantReqs, 0.35, 2048, tt.wantRetry)
		})
	}
}
