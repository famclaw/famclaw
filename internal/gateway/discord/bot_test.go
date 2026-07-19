package discord

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	assert.Equal(t, "subdir/test.txt", filePath)

	// Verify the file was written correctly
	content, err := os.ReadFile(filepath.Join(tempDir, "subdir", "test.txt"))
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