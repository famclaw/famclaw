package gateway

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestInvalidChatError(t *testing.T) {
	cause := errors.New("404: Unknown Channel (code: 0)")
	ice := &InvalidChatError{Gateway: "discord", ChatID: "12345", Cause: cause}

	cases := []struct {
		name    string
		err     error
		wantIs  bool // errors.Is(err, ErrInvalidChat)
		wantMsg string
	}{
		{
			name:    "direct match",
			err:     ice,
			wantIs:  true,
			wantMsg: "discord chat 12345",
		},
		{
			name:    "wrapped match",
			err:     fmt.Errorf("sending discord message: %w", ice),
			wantIs:  true,
			wantMsg: "chat no longer exists",
		},
		{
			name:   "transient error does not match",
			err:    errors.New("502 bad gateway"),
			wantIs: false,
		},
		{
			name:   "plain error does not match",
			err:    errors.New("unknown channel"),
			wantIs: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(tc.err, ErrInvalidChat); got != tc.wantIs {
				t.Errorf("errors.Is(%v, ErrInvalidChat) = %v, want %v", tc.err, got, tc.wantIs)
			}
			if tc.wantMsg != "" {
				if !strings.Contains(tc.err.Error(), tc.wantMsg) {
					t.Errorf("Error() = %q, want it to contain %q", tc.err.Error(), tc.wantMsg)
				}
			}
		})
	}

	// Unwrap exposes the platform API error.
	var got *InvalidChatError
	if !errors.As(ice, &got) {
		t.Fatal("errors.As did not recover *InvalidChatError")
	}
	if got.Cause != cause {
		t.Errorf("Unwrap chain: Cause = %v, want %v", got.Cause, cause)
	}

	// A nil cause must not panic and must still match the sentinel.
	n := &InvalidChatError{Gateway: "telegram", ChatID: "9"}
	if !errors.Is(n, ErrInvalidChat) {
		t.Error("nil-cause InvalidChatError should still match ErrInvalidChat")
	}
	if n.Error() == "" {
		t.Error("nil-cause Error() should not be empty")
	}
}
