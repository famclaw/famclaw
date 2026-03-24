package identity

// OnboardingMessage returns the message shown to unregistered gateway accounts.
func OnboardingMessage() string {
	return "Welcome to FamClaw! Your account isn't linked yet.\n\n" +
		"Ask a parent to add your account in the FamClaw dashboard:\n" +
		"1. Open the FamClaw web UI (famclaw.local:8080)\n" +
		"2. Go to the Parent Dashboard\n" +
		"3. Link your messaging account to your FamClaw profile\n\n" +
		"Once linked, you can start chatting!"
}

// UnknownAccountMessage returns a short message for repeated unknown account attempts.
func UnknownAccountMessage() string {
	return "I don't recognize this account. Please ask a parent to link it in the FamClaw dashboard."
}
