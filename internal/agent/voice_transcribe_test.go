package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// mockTranscriber is a test double for the Transcriber interface.
type mockTranscriber struct {
	transcript string
	err        error
	calls      int
}

func (m *mockTranscriber) TranscribeAudio(_ context.Context, _ []byte, _ string) (string, error) {
	m.calls++
	if m.err != nil {
		return "", m.err
	}
	return m.transcript, nil
}

// setupAgentForVoice creates an agent wired with the given transcriber and
// message context. The user is always "parent" (auto-allow policy) so the
// LLM is actually invoked, letting us inspect what reached the gates.
func setupAgentForVoice(t *testing.T, serverURL string, transcriber Transcriber, msgCtx gateway.MsgContext) *Agent {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	clf := classifier.New()

	cfg := &config.Config{
		LLM: config.LLMConfig{
			BaseURL:           serverURL,
			Model:             "test",
			Temperature:       0.7,
			MaxResponseTokens: 100,
			MaxContextTokens:  4096,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent"},
		},
	}

	user := &cfg.Users[0]
	client := llm.NewClient(serverURL, "test", "")
	a, err := NewAgent(user, cfg, client, ev, clf, db, AgentDeps{
		Transcriber: transcriber,
		MsgContext:  msgCtx,
	})
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}
	return a
}

// capturingLLMServer returns an httptest server that records every
// request body in capturedBodies and the last non-empty user-message
// content in capturedUserMsg. It replies with a canned response:
// SSE stream when the request asks for stream=true, non-streaming JSON
// otherwise.
func capturingLLMServer(t *testing.T, reply string) (*httptest.Server, *string, *[]string) {
	t.Helper()
	var capturedUserMsg string
	var capturedBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "test"}}})
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
			return
		}
		capturedBodies = append(capturedBodies, string(raw))

		// Determine stream mode and extract user messages.
		var req struct {
			Stream   bool                         `json:"stream"`
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("unmarshaling request: %v", err)
		}
		for _, m := range req.Messages {
			roleRaw, hasRole := m["role"]
			if !hasRole {
				continue
			}
			var role string
			_ = json.Unmarshal(roleRaw, &role)
			if role != "user" {
				continue
			}
			contentRaw, hasContent := m["content"]
			if !hasContent {
				continue
			}
			// Content may be a string or an array of {type, text} parts.
			if text := extractUserText(contentRaw); text != "" {
				capturedUserMsg = text
			}
		}

		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			chunk := map[string]any{
				"choices": []map[string]any{{
					"delta": map[string]any{"content": reply},
				}},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		// Non-streaming response.
		resp := map[string]any{
			"choices": []map[string]any{{
				"message":       llm.Message{Role: "assistant", Content: reply},
				"finish_reason": "stop",
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	return srv, &capturedUserMsg, &capturedBodies
}

// TestVoiceTranscriptReachesLlmInput proves the gate-safety property:
// the transcript becomes the user message that reaches the LLM (and thus
// Turn.Input, which both StageClassify and StagePolicyInput read).
func TestVoiceTranscriptReachesLlmInput(t *testing.T) {
	srv, captured, _ := capturingLLMServer(t, "ok")
	defer srv.Close()

	transcriber := &mockTranscriber{transcript: "what time is it"}
	msgCtx := gateway.MsgContext{
		Gateway: "telegram",
		Attachments: []gateway.Attachment{{
			Type:     "audio",
			Data:     "ZmFrZS1vZ2ctYnl0ZXM=", // base64 of "fake-ogg-bytes"
			MIMEType: "audio/ogg",
		}},
	}
	agent := setupAgentForVoice(t, srv.URL, transcriber, msgCtx)

	resp, err := agent.Chat(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.PolicyAction != "allow" {
		t.Fatalf("action = %q, want allow", resp.PolicyAction)
	}
	if captured == nil || *captured != "what time is it" {
		t.Errorf("LLM user message = %q, want %q", *captured, "what time is it")
	}
	if transcriber.calls != 1 {
		t.Errorf("transcriber called %d times, want 1", transcriber.calls)
	}
}

// TestVoiceTranscriptSavedInHistory proves the transcript is persisted to
// conversation history (not an empty string) via SaveMessage, which runs
// after transcription.
func TestVoiceTranscriptSavedInHistory(t *testing.T) {
	srv, _, _ := capturingLLMServer(t, "ok")
	defer srv.Close()

	transcriber := &mockTranscriber{transcript: "hello from voice"}
	msgCtx := gateway.MsgContext{
		Gateway: "telegram",
		Attachments: []gateway.Attachment{{
			Type:     "audio",
			Data:     "ZmFrZS1vZ2ctYnl0ZXM=",
			MIMEType: "audio/ogg",
		}},
	}
	agent := setupAgentForVoice(t, srv.URL, transcriber, msgCtx)

	if _, err := agent.Chat(context.Background(), "", nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	history, err := agent.db.GetConversationHistory(agent.convID, 20)
	if err != nil {
		t.Fatalf("GetConversationHistory: %v", err)
	}
	var foundTranscript bool
	for _, m := range history {
		if m.Role == "user" && m.Content == "hello from voice" {
			foundTranscript = true
		}
		if m.Role == "user" && m.Content == "" {
			t.Errorf("found empty user message in history — transcript not saved")
		}
	}
	if !foundTranscript {
		t.Errorf("transcript %q not found in conversation history", "hello from voice")
	}
}

// TestVoiceTranscriptionFailureVisible proves a transcription error produces
// a visible "voice isn't available" reply — never a silent drop.
func TestVoiceTranscriptionFailureVisible(t *testing.T) {
	srv, captured, _ := capturingLLMServer(t, "ok")
	defer srv.Close()

	transcriber := &mockTranscriber{err: assertErr("transcription service down")}
	msgCtx := gateway.MsgContext{
		Gateway: "telegram",
		Attachments: []gateway.Attachment{{
			Type:     "audio",
			Data:     "ZmFrZS1vZ2ctYnl0ZXM=",
			MIMEType: "audio/ogg",
		}},
	}
	agent := setupAgentForVoice(t, srv.URL, transcriber, msgCtx)

	resp, err := agent.Chat(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.HasPrefix(resp.Content, "voice isn't available") {
		t.Errorf("content = %q, want prefix %q", resp.Content, "voice isn't available")
	}
	// The LLM must never be called when transcription fails.
	if captured != nil && *captured != "" {
		t.Errorf("LLM was called despite transcription failure (captured=%q)", *captured)
	}
}

// TestVoiceTranscriberNilVisible proves that with no transcriber configured
// (disabled), an audio attachment produces a visible message — not silence.
func TestVoiceTranscriberNilVisible(t *testing.T) {
	srv, captured, _ := capturingLLMServer(t, "ok")
	defer srv.Close()

	msgCtx := gateway.MsgContext{
		Gateway: "telegram",
		Attachments: []gateway.Attachment{{
			Type:     "audio",
			Data:     "ZmFrZS1vZ2ctYnl0ZXM=",
			MIMEType: "audio/ogg",
		}},
	}
	// transcriber is nil → disabled.
	agent := setupAgentForVoice(t, srv.URL, nil, msgCtx)

	resp, err := agent.Chat(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.HasPrefix(resp.Content, "voice isn't available") {
		t.Errorf("content = %q, want prefix %q", resp.Content, "voice isn't available")
	}
	if captured != nil && *captured != "" {
		t.Errorf("LLM was called despite disabled transcription (captured=%q)", *captured)
	}
}

// TestImageAttachmentUnchanged proves that image attachments are NOT routed
// through the transcription path — they remain on the multimodal path and
// still reach the LLM unchanged.
func TestImageAttachmentUnchanged(t *testing.T) {
	srv, _, capturedBodies := capturingLLMServer(t, "ok")
	defer srv.Close()

	imgData := "iVBtb2NrLWJhc2U2NA" // neutral synthetic base64
	msgCtx := gateway.MsgContext{
		Gateway: "telegram",
		Attachments: []gateway.Attachment{{
			Type:     "image",
			Data:     imgData,
			MIMEType: "image/png",
		}},
	}
	// No transcriber — images must work regardless.
	agent := setupAgentForVoice(t, srv.URL, nil, msgCtx)

	resp, err := agent.Chat(context.Background(), "describe this", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if strings.HasPrefix(resp.Content, "voice isn't available") {
		t.Errorf("image message should not trigger voice unavailable: %q", resp.Content)
	}
	if resp.PolicyAction != "allow" {
		t.Fatalf("action = %q, want allow", resp.PolicyAction)
	}
	// The image base64 must appear in one of the LLM request bodies,
	// proving the multimodal path was followed (not the transcription path).
	var foundImg bool
	for _, body := range *capturedBodies {
		if strings.Contains(body, imgData) {
			foundImg = true
		}
	}
	if !foundImg {
		t.Errorf("image data not found in any LLM request body — image path broken")
	}
}

// TestTextOnlyMessageUnaffected proves a plain text message with no
// attachments is forwarded to the LLM verbatim — the transcription
// logic is a no-op.
func TestTextOnlyMessageUnaffected(t *testing.T) {
	srv, captured, _ := capturingLLMServer(t, "ok")
	defer srv.Close()

	agent := setupAgentForVoice(t, srv.URL, nil, gateway.MsgContext{Gateway: "telegram"})

	resp, err := agent.Chat(context.Background(), "hello world", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if *captured != "hello world" {
		t.Errorf("LLM user message = %q, want %q", *captured, "hello world")
	}
	if resp.PolicyAction != "allow" {
		t.Errorf("action = %q, want allow", resp.PolicyAction)
	}
}

// TestTranscribeAttachments_NoAudioReturnsEmpty is a unit test for the
// transcribeAttachments helper: non-audio attachments produce no transcript.
func TestTranscribeAttachments_NoAudioReturnsEmpty(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	clf := classifier.New()
	cfg := &config.Config{
		LLM:   config.LLMConfig{BaseURL: "http://x", Model: "t", MaxResponseTokens: 1, MaxContextTokens: 1},
		Users: []config.UserConfig{{Name: "parent", Role: "parent"}},
	}
	tr := &mockTranscriber{transcript: "should-not-be-used"}
	a, err := NewAgent(&cfg.Users[0], cfg, nil, ev, clf, db, AgentDeps{Transcriber: tr})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.transcribeAttachments(context.Background(), []gateway.Attachment{
		{Type: "image", Data: "abc", MIMEType: "image/png"},
	}, "caption text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty transcript for image-only, got %q", got)
	}
	if tr.calls != 0 {
		t.Errorf("transcriber should not be called for image-only, got %d calls", tr.calls)
	}
}

// extractUserText pulls the text content from a message's "content" field,
// which may be a plain string or an array of {type, text} parts (multimodal).
func extractUserText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err == nil {
		for _, p := range parts {
			if p["type"] == "text" {
				if text, ok := p["text"].(string); ok && text != "" {
					return text
				}
			}
		}
	}
	return ""
}

// assertErr is a simple error type for tests.
type assertErr string

func (e assertErr) Error() string { return string(e) }
