package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestApprovalJSONSerializationRegression(t *testing.T) {
	// Create a sample Approval struct with test data
	approval := &Approval{
		ID:           "test-id-123",
		UserName:     "john_doe",
		UserDisplay:  "John Doe",
		AgeGroup:     "teen",
		Category:     "education",
		QueryText:    "What is the capital of France?",
		Status:       "pending",
		CreatedAt:    time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2023, 1, 1, 13, 0, 0, 0, time.UTC),
		ExpiresAt:    time.Date(2023, 1, 2, 12, 0, 0, 0, time.UTC),
		DecidedBy:    "parent123",
		DecisionNote: "Approved for educational purposes",
	}

	// Serialize to JSON
	jsonData, err := json.Marshal(approval)
	if err != nil {
		t.Fatalf("Failed to marshal Approval to JSON: %v", err)
	}

	// Parse the JSON to verify structure
	var parsed map[string]interface{}
	err = json.Unmarshal(jsonData, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	// Verify that all fields are present with correct lowercase names (regression test for issue #198)
	fieldNames := []string{"id", "user_name", "user_display", "age_group", "category", "query_text", "status", "created_at", "updated_at", "expires_at", "decided_by", "decision_note"}
	
	for _, fieldName := range fieldNames {
		if _, exists := parsed[fieldName]; !exists {
			t.Errorf("Expected lowercase field '%s' not found in JSON", fieldName)
		}
	}

	// Verify no uppercase field names exist (regression check)
	for fieldName := range parsed {
		// Check if any field name contains uppercase letters
		if containsUppercase(fieldName) {
			t.Errorf("Field name '%s' contains uppercase letters - this indicates the bug from issue #198 is present", fieldName)
		}
	}
}

func containsUppercase(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}