package classifier

import "testing"

func TestClassify(t *testing.T) {
	clf := New()

	tests := []struct {
		name  string
		input string
		want  Category
	}{
		// ── Edge cases ───────────────────────────────────────────────────
		{"empty string", "", General},
		{"whitespace only", "   ", General},
		{"gibberish", "asdjfklasdjf", General},
		{"single letter", "a", General},
		{"numbers only", "12345", General},

		// ── General (fallback) ───────────────────────────────────────────
		{"general question", "what is the weather today", General},
		{"hello", "hello how are you", General},

		// ── Science ──────────────────────────────────────────────────────
		{"science basic", "tell me about science experiments", Science},
		{"science dna", "how does DNA work", Science},
		{"science photosynthesis", "explain photosynthesis", Science},

		// ── Math ─────────────────────────────────────────────────────────
		{"math basic", "help me with math homework", Math},
		{"math algebra", "solve this algebra equation", Math},
		{"math fractions", "how do I add fractions", Math},

		// ── History ──────────────────────────────────────────────────────
		{"history basic", "tell me about history", History},
		{"history ww2", "what happened in world war 2", History},
		{"history rome", "tell me about the roman empire", History},

		// ── Arts ─────────────────────────────────────────────────────────
		{"arts drawing", "how to start drawing", Arts},
		{"arts piano", "teach me piano", Arts},
		{"arts origami", "how to make origami crane", Arts},

		// ── Sports ───────────────────────────────────────────────────────
		{"sports soccer", "who won the soccer game", Sports},
		{"sports olympics", "when are the olympics", Sports},

		// ── Technology ───────────────────────────────────────────────────
		{"tech coding", "teach me programming", Technology},
		{"tech python", "how to learn python", Technology},

		// ── Health (low risk) ────────────────────────────────────────────
		{"health nutrition", "what vitamins should I take", Health},
		{"health exercise", "best exercise for kids", Health},

		// ── Social media (medium risk) ───────────────────────────────────
		{"social tiktok", "how to use tiktok", SocialMedia},
		{"social instagram", "can I make an instagram account", SocialMedia},

		// ── Dating (medium risk) ─────────────────────────────────────────
		{"dating crush", "I have a crush on someone", Dating},
		{"dating boyfriend", "how to get a boyfriend", Dating},

		// ── Religion (medium risk) ───────────────────────────────────────
		{"religion god", "does god exist", Religion},
		{"religion bible", "what does the bible say", Religion},

		// ── Politics (medium risk) ───────────────────────────────────────
		{"politics election", "who won the election", Politics},
		{"politics president", "who is the president", Politics},

		// ── Finance (medium risk) ────────────────────────────────────────
		{"finance bitcoin", "what is bitcoin", Finance},
		{"finance stocks", "how does the stock market work", Finance},

		// ── Mental health (high risk) ────────────────────────────────────
		{"mental depression", "I feel depressed", MentalHealth},
		{"mental anxiety", "I have anxiety", MentalHealth},
		{"mental panic", "having a panic attack", MentalHealth},

		// ── Violence (high risk) ─────────────────────────────────────────
		{"violence weapon", "how to use a weapon", Violence},
		{"violence gun", "tell me about guns", Violence},

		// ── Drugs (high risk) ────────────────────────────────────────────
		{"drugs marijuana", "what is marijuana", Drugs},
		{"drugs alcohol", "can I drink alcohol", Drugs},

		// ── Gambling (high risk) ─────────────────────────────────────────
		{"gambling casino", "how to play casino games", Gambling},
		{"gambling betting", "sports betting tips", Gambling},

		// ── Hacking (high risk) ──────────────────────────────────────────
		{"hacking basic", "how to hack into wifi", Hacking},
		{"hacking malware", "what is malware", Hacking},

		// ── Sexual content (critical) ────────────────────────────────────
		{"sexual porn", "show me porn", SexualContent},
		{"sexual explicit", "explicit content", SexualContent},

		// ── Self-harm (critical) ─────────────────────────────────────────
		{"self-harm suicide", "I want to commit suicide", SelfHarm},
		{"self-harm cutting", "I've been cutting myself", SelfHarm},
		{"self-harm die", "I want to die", SelfHarm},

		// ── Hate speech (critical) ───────────────────────────────────────
		{"hate racist", "teach me racist jokes", HateSpeech},
		{"hate slur", "what are racial slurs", HateSpeech},

		// ── Illegal activity (critical) ──────────────────────────────────
		{"illegal steal", "how to steal from a store", IllegalActivity},
		{"illegal bomb", "how to make a bomb", IllegalActivity},

		// ── Case insensitivity ───────────────────────────────────────────
		{"upper case", "TELL ME ABOUT SCIENCE", Science},
		{"mixed case", "How Does DNA Work", Science},

		// ── Priority: critical beats lower ───────────────────────────────
		{"critical over general", "tell me about suicide prevention in history", SelfHarm},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clf.Classify(tt.input)
			if got != tt.want {
				t.Errorf("Classify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func BenchmarkClassify(b *testing.B) {
	clf := New()
	for i := 0; i < b.N; i++ {
		clf.Classify("I want to learn about science experiments and chemistry")
	}
}
