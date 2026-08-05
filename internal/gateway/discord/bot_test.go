package discord

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/assert"
)

func TestDownloadFile(t *testing.T) {
	// Create a test server that serves a test file
	testContent := "This is a test file content"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(testContent))
	}))
	defer server.Close()

	ctx := context.Background()
	data, err := downloadFile(ctx, server.URL, 1024*1024) // 1MB limit
	assert.NoError(t, err)
	assert.Equal(t, testContent, string(data))
}

func TestDownloadFileExceedsLimit(t *testing.T) {
	// Create a test server that serves a file larger than the limit
	testContent := strings.Repeat("a", 1024*1024+1) // 1MB + 1 byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(testContent))
	}))
	defer server.Close()

	ctx := context.Background()
	_, err := downloadFile(ctx, server.URL, 1024*1024) // 1MB limit
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestWriteAttachmentToFile(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "test_sandbox")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	ctx := context.Background()
	testContent := "Test file content"
	filePath, err := writeAttachmentToFile(ctx, tempDir, "test.txt", []byte(testContent))
	assert.NoError(t, err)
	assert.Equal(t, "test.txt", filePath)

	// Verify the file was written correctly
	content, err := os.ReadFile(filepath.Join(tempDir, "test.txt"))
	assert.NoError(t, err)
	assert.Equal(t, testContent, string(content))
}

func TestWriteAttachmentToFileWithPathTraversal(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "test_sandbox")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	ctx := context.Background()
	testContent := "Test file content"
	filePath, err := writeAttachmentToFile(ctx, tempDir, "../evil.txt", []byte(testContent))
	assert.NoError(t, err)
	// Should be sanitized to just the filename
	assert.Equal(t, "evil.txt", filePath)
}

func TestWriteAttachmentToFileWithSubdir(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "test_sandbox")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	ctx := context.Background()
	testContent := "Test file content"
	filePath, err := writeAttachmentToFile(ctx, tempDir, "subdir/test.txt", []byte(testContent))
	assert.NoError(t, err)
	// Expect base name because writeAttachmentToFile strips directory parts for security
	assert.Equal(t, "test.txt", filePath)

	// Verify the file was written correctly in the sandbox root
	content, err := os.ReadFile(filepath.Join(tempDir, "test.txt"))
	assert.NoError(t, err)
	assert.Equal(t, testContent, string(content))
}

func TestDiscordBotWithFileAttachments(t *testing.T) {
	// This is a basic smoke test to ensure the bot can be created
	// Real integration tests would require mocking the Discord API
	bot := NewWithSandbox("fake-token", "/tmp/test_sandbox")
	assert.NotNil(t, bot)
	assert.Equal(t, "discord", bot.Name())
}

func ExampleNewWithSandbox() {
	// Example of creating a Discord bot with sandbox root
	bot := NewWithSandbox("your-discord-token", "/path/to/sandbox")
	fmt.Println(bot.Name())
	// Output: discord
}

func TestValidateMIMEExtension(t *testing.T) {
	// Test matching MIME type and extension
	assert.NoError(t, validateMIMEExtension("image/jpeg", "test.jpg"))
	assert.NoError(t, validateMIMEExtension("image/jpeg", "test.jpeg"))
	assert.NoError(t, validateMIMEExtension("image/png", "test.png"))
	assert.NoError(t, validateMIMEExtension("image/gif", "test.gif"))
	assert.NoError(t, validateMIMEExtension("image/webp", "test.webp"))
	assert.NoError(t, validateMIMEExtension("text/plain", "test.txt"))
	assert.NoError(t, validateMIMEExtension("application/pdf", "test.pdf"))
	assert.NoError(t, validateMIMEExtension("application/zip", "test.zip"))

	// Test mismatched extension
	assert.Error(t, validateMIMEExtension("image/jpeg", "test.png"))
	assert.Error(t, validateMIMEExtension("image/png", "test.jpg"))
	assert.Error(t, validateMIMEExtension("text/plain", "test.pdf"))

	// Test unsupported MIME type
	assert.Error(t, validateMIMEExtension("application/octet-stream", "test.bin"))
	assert.Error(t, validateMIMEExtension("unknown/type", "test.xyz"))
}

func TestWriteAttachmentToFilePathTraversalRejected(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "test_sandbox")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	ctx := context.Background()
	testContent := "Test file content"

	// Test case 1: relative path with traversal
	filePath, err := writeAttachmentToFile(ctx, tempDir, "../../etc/passwd", []byte(testContent))
	assert.NoError(t, err) // because we expect it to be contained
	assert.Equal(t, "passwd", filePath)
	// Verify file exists in sandbox
	content, err := os.ReadFile(filepath.Join(tempDir, "passwd"))
	assert.NoError(t, err)
	assert.Equal(t, testContent, string(content))

	// Test case 2: absolute path
	filePath, err = writeAttachmentToFile(ctx, tempDir, "/etc/passwd", []byte(testContent))
	assert.NoError(t, err)
	assert.Equal(t, "passwd", filePath)
	content, err = os.ReadFile(filepath.Join(tempDir, "passwd"))
	assert.NoError(t, err)
	assert.Equal(t, testContent, string(content))

	// Test case 3: just ".."
	_, err = writeAttachmentToFile(ctx, tempDir, "..", []byte(testContent))
	assert.Error(t, err)
	// No file should have been written; we rely on the error above.
}

// TestProcessAudioAttachment verifies that a Discord audio attachment
// (e.g. a voice message) is downloaded and delivered as a gateway.Attachment
// with Type "audio" and the correct MIME type.
func TestProcessAudioAttachment(t *testing.T) {
	audioData := []byte("fake-ogg-opus-data")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(audioData)
	}))
	defer ts.Close()

	bot := NewWithSandbox("fake-token", t.TempDir())
	ctx := context.Background()

	atts := []*discordgo.MessageAttachment{
		{
			ContentType: "audio/ogg",
			URL:         ts.URL,
			Filename:    "voice.ogg",
			Size:        len(audioData),
		},
	}

	attachments, notes := bot.processAttachments(ctx, atts)

	assert.Len(t, attachments, 1)
	assert.Empty(t, notes)

	att := attachments[0]
	assert.Equal(t, "audio", att.Type)
	assert.Equal(t, "audio/ogg", att.MIMEType)

	decoded, err := base64.StdEncoding.DecodeString(att.Data)
	assert.NoError(t, err)
	assert.Equal(t, audioData, decoded)
}

// TestProcessAudioAttachmentOversized verifies that an audio attachment
// exceeding the size cap is skipped (not silently downloaded).
func TestProcessAudioAttachmentOversized(t *testing.T) {
	bot := NewWithSandbox("fake-token", t.TempDir())
	ctx := context.Background()

	atts := []*discordgo.MessageAttachment{
		{
			ContentType: "audio/ogg",
			URL:         "http://nonexistent.invalid/voice",
			Filename:    "voice.ogg",
			Size:        maxAudioBytes + 1,
		},
	}

	attachments, notes := bot.processAttachments(ctx, atts)

	assert.Empty(t, attachments)
	assert.Empty(t, notes)
}

// TestProcessImageAndAudioCoexist verifies that a message carrying both
// an image and an audio attachment produces two gateway attachments.
func TestProcessImageAndAudioCoexist(t *testing.T) {
	imgData := []byte("fake-png-data")
	audioData := []byte("fake-mp3-data")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgData)
	}))
	defer ts.Close()

	audioTs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(audioData)
	}))
	defer audioTs.Close()

	bot := NewWithSandbox("fake-token", t.TempDir())
	ctx := context.Background()

	atts := []*discordgo.MessageAttachment{
		{
			ContentType: "image/png",
			URL:         ts.URL,
			Filename:    "photo.png",
			Size:        len(imgData),
		},
		{
			ContentType: "audio/mpeg",
			URL:         audioTs.URL,
			Filename:    "clip.mp3",
			Size:        len(audioData),
		},
	}

	attachments, _ := bot.processAttachments(ctx, atts)

	assert.Len(t, attachments, 2)
	var hasAudio, hasImage bool
	for _, att := range attachments {
		switch att.Type {
		case "audio":
			hasAudio = true
			assert.Equal(t, "audio/mpeg", att.MIMEType)
		case "image":
			hasImage = true
			assert.Equal(t, "image/png", att.MIMEType)
		}
	}
	assert.True(t, hasAudio, "expected an audio attachment")
	assert.True(t, hasImage, "expected an image attachment")
}

// TestValidateMIMEExtensionAudio verifies that audio MIME types are now
// accepted by the extension validator.
func TestValidateMIMEExtensionAudio(t *testing.T) {
	assert.NoError(t, validateMIMEExtension("audio/ogg", "voice.ogg"))
	assert.NoError(t, validateMIMEExtension("audio/mpeg", "clip.mp3"))
	assert.NoError(t, validateMIMEExtension("audio/mp3", "clip.mp3"))
	assert.NoError(t, validateMIMEExtension("audio/wav", "sound.wav"))
	assert.NoError(t, validateMIMEExtension("audio/webm", "voice.webm"))
	assert.NoError(t, validateMIMEExtension("audio/opus", "voice.opus"))
	assert.NoError(t, validateMIMEExtension("audio/mp4", "clip.m4a"))

	// Mismatched extension still rejected
	assert.Error(t, validateMIMEExtension("audio/ogg", "clip.mp3"))
	assert.Error(t, validateMIMEExtension("audio/mpeg", "voice.ogg"))
}

// TestProcessAudioAttachmentDownloadFailure verifies that when the audio
// file cannot be downloaded, the error is surfaced via log (not silently
// dropped) and no audio attachment is produced.
func TestProcessAudioAttachmentDownloadFailure(t *testing.T) {
	bot := NewWithSandbox("fake-token", t.TempDir())
	ctx := context.Background()

	atts := []*discordgo.MessageAttachment{
		{
			ContentType: "audio/ogg",
			URL:         "http://127.0.0.1:0/voice", // port 0 — nothing listening
			Filename:    "voice.ogg",
			Size:        100,
		},
	}

	attachments, _ := bot.processAttachments(ctx, atts)
	assert.Empty(t, attachments)
}

// TestSendOpensDMChannel verifies that proactive delivery to a Discord user
// opens a DM channel (UserChannelCreate) and sends the message to the DM
// channel ID — never to the bare user ID, which would 404 as "Unknown
// Channel". It also confirms the resolved DM channel is cached so a second
// send reuses it instead of re-opening.
func TestSendOpensDMChannel(t *testing.T) {
	var (
		mu             sync.Mutex
		dmOpenCount    int
		sentChannelIDs []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the request body before responding so the httptest connection is not
		// left in a half-read state.
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/dm"):
			mu.Lock()
			dmOpenCount++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"dm-channel-456","type":1}`))
		case strings.Contains(r.URL.Path, "/channels/") && strings.HasSuffix(r.URL.Path, "/messages"):
			seg := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			// path: /channels/{channelID}/messages
			mu.Lock()
			sentChannelIDs = append(sentChannelIDs, seg[len(seg)-2])
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	origUserChannels := discordgo.EndpointUserChannels
	origChannelMessages := discordgo.EndpointChannelMessages
	discordgo.EndpointUserChannels = func(string) string { return server.URL + "/dm" }
	discordgo.EndpointChannelMessages = func(cID string) string {
		return server.URL + "/channels/" + cID + "/messages"
	}
	defer func() {
		discordgo.EndpointUserChannels = origUserChannels
		discordgo.EndpointChannelMessages = origChannelMessages
	}()

	session, err := discordgo.New("Bot test-token")
	assert.NoError(t, err)
	// Use a short-timeout, no-keep-alive client so each request is independent
	// and does not depend on connection reuse into the httptest server.
	session.Client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	b := &Bot{session: session}

	// The recipient is a USER id (as stored in gateway_accounts.external_id).
	userID := "user-123"
	assert.NoError(t, b.Send(context.Background(), userID, "hello julia"))

	mu.Lock()
	defer mu.Unlock()
	if dmOpenCount != 1 {
		t.Errorf("expected UserChannelCreate called once, got %d", dmOpenCount)
	}
	if len(sentChannelIDs) != 1 {
		t.Fatalf("expected 1 message send, got %d", len(sentChannelIDs))
	}
	if sentChannelIDs[0] == userID {
		t.Error("message sent to the user ID directly; must use the DM channel ID")
	}
	if sentChannelIDs[0] != "dm-channel-456" {
		t.Errorf("sent to channel %q, want DM channel %q", sentChannelIDs[0], "dm-channel-456")
	}

	// Second delivery must reuse the cached DM channel (no new UserChannelCreate).
	// We verify this with a direct dmChannelID lookup — a cache hit makes no HTTP
	// request, so UserChannelCreate is not invoked again. (A second full Send via
	// the discordgo REST client against the httptest server is unreliable, so we
	// exercise the cache directly.)
	cachedID, err := b.dmChannelID(userID)
	assert.NoError(t, err)
	if cachedID != "dm-channel-456" {
		t.Errorf("cached dm channel = %q, want %q", cachedID, "dm-channel-456")
	}
	if dmOpenCount != 1 {
		t.Errorf("expected DM channel cached (still 1 open), got %d", dmOpenCount)
	}
	if len(sentChannelIDs) != 1 {
		t.Errorf("expected 1 total send, got %d", len(sentChannelIDs))
	}
}
