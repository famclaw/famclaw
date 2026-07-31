package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateSandbox_ResumesAfterCrashBetweenRenameAndMkdir(t *testing.T) {
	// This test verifies the crash-resume scenario described in the review
	// Create exact post-crash state by hand:
	// - Create base+".migrating" containing users/alice/diary.txt, a loose.txt, and a conversations/ subtree
	// - Ensure base does NOT exist
	// - Call migrateSandboxIfNecessary(base) and assert it succeeds
	// - Verify conversations subtree is back under base
	// - Verify loose file is under conversations/_legacy/root
	// - Verify user file is under conversations/_legacy/users
	// - Verify staging is gone
	// - Verify marker exists

	// Setup test directories
	base := t.TempDir()
	staging := base + ".migrating"
	
	// Create the "crashed" state manually:
	// 1. Create staging directory with the expected structure
	err := os.MkdirAll(staging, 0755)
	require.NoError(t, err)
	
	// Create users/alice/diary.txt
	aliceDir := filepath.Join(staging, "users", "alice")
	err = os.MkdirAll(aliceDir, 0755)
	require.NoError(t, err)
	
	aliceDiary := filepath.Join(aliceDir, "diary.txt")
	err = os.WriteFile(aliceDiary, []byte("Alice's diary content"), 0644)
	require.NoError(t, err)
	
	// Create a loose.txt file
	looseFile := filepath.Join(staging, "loose.txt")
	err = os.WriteFile(looseFile, []byte("loose file content"), 0644)
	require.NoError(t, err)
	
	// Create conversations subtree
	conversationsDir := filepath.Join(staging, "conversations")
	err = os.MkdirAll(conversationsDir, 0755)
	require.NoError(t, err)
	
	// Create a conversation file
	conversationFile := filepath.Join(conversationsDir, "conversation.txt")
	err = os.WriteFile(conversationFile, []byte("conversation content"), 0644)
	require.NoError(t, err)
	
	// Ensure base directory does NOT exist (this simulates the crash scenario)
	err = os.RemoveAll(base)
	assert.NoError(t, err)
	
	// Now call migrateSandboxIfNecessary - this should handle the resume correctly
	err = migrateSandboxIfNecessary(base)
	assert.NoError(t, err)
	
	// Verify that base directory now exists
	assert.DirExists(t, base)
	
	// Verify conversations subtree is back under base
	conversationsBack := filepath.Join(base, "conversations")
	assert.DirExists(t, conversationsBack)
	
	// Verify the conversation file is in the right location
	convFileBack := filepath.Join(conversationsBack, "conversation.txt")
	assert.FileExists(t, convFileBack)
	
	// Verify loose.txt is under conversations/_legacy/root
	legacyRoot := filepath.Join(base, "conversations", "_legacy", "root")
	looseFileBack := filepath.Join(legacyRoot, "loose.txt")
	assert.FileExists(t, looseFileBack)
	
	// Verify alice's diary is under conversations/_legacy/users
	legacyUsers := filepath.Join(base, "conversations", "_legacy", "users", "alice")
	aliceDiaryBack := filepath.Join(legacyUsers, "diary.txt")
	assert.FileExists(t, aliceDiaryBack)
	
	// Verify staging directory is gone
	assert.NoDirExists(t, staging)
	
	// Verify marker file exists
	markerFile := filepath.Join(base, ".sandbox_migrated")
	assert.FileExists(t, markerFile)
}