package identity

// OnboardingMessage returns the message shown to unregistered gateway accounts.
// Reachable only as the inner-loop fallback when a user disappears between
// Handle() and process(); the primary unknown-account flow is now in
// router.handleUnknownAccount which auto-links by display name or shows
// a numbered list.
func OnboardingMessage() string {
	return "Welcome to FamClaw! Your account isn't linked yet.\n\n" +
		"Ask a parent to add your account in the FamClaw dashboard, or " +
		"message me again — I can usually figure out who you are from your " +
		"profile name."
}

// UnknownAccountMessage returns a short message for repeated unknown account attempts.
func UnknownAccountMessage() string {
	return "I don't recognize this account. Please ask a parent to link it in the FamClaw dashboard."
}
