// Package classifier provides keyword-based topic classification.
// No LLM calls — pure keyword matching, fast (<1ms per call).
package classifier

import "strings"

// Classifier categorizes user messages by topic using keyword matching.
type Classifier struct {
	rules []rule
}

type rule struct {
	category Category
	keywords []string
}

// New creates a Classifier with keyword rules for all categories.
func New() *Classifier {
	return &Classifier{
		rules: []rule{
			{SelfHarm, []string{
				"suicide", "kill myself", "end my life", "want to die",
				"self-harm", "self harm", "cutting myself", "hurt myself",
				"don't want to live", "no reason to live",
			}},
			{HateSpeech, []string{
				"hate speech", "racial slur", "slur", "racist",
				"bigot", "discrimination", "white supremac",
				"nazi", "antisemit", "homophob", "transphob",
				"ethnic cleansing", "genocide",
			}},
			{SexualContent, []string{
				"pornography", "porn", "sexual content", "nude",
				"explicit content", "xxx", "onlyfans", "sex video",
				"sexual intercourse", "erotic",
			}},
			{IllegalActivity, []string{
				"illegal", "how to steal", "make a bomb",
				"counterfeit", "forge document", "pick a lock",
				"smuggling", "trafficking", "launder money",
				"break into", "hotwire",
			}},
			{Violence, []string{
				"violence", "violent", "weapon", "gun", "firearm",
				"knife fight", "how to fight", "assault",
				"murder", "kill someone", "shoot", "stab",
				"bomb", "explosive",
			}},
			{Drugs, []string{
				"drug", "cocaine", "heroin", "meth", "marijuana",
				"weed", "cannabis", "opioid", "overdose",
				"alcohol", "drunk", "beer", "vodka", "whiskey",
				"substance abuse", "getting high", "smoking",
			}},
			{Gambling, []string{
				"gambling", "gamble", "casino", "betting",
				"poker", "slot machine", "blackjack",
				"sports bet", "lottery", "wager",
			}},
			{Hacking, []string{
				"hacking", "hack into", "exploit", "vulnerability",
				"password crack", "brute force", "phishing",
				"malware", "ransomware", "ddos", "sql injection",
				"unauthorized access", "reverse engineer",
			}},
			{MentalHealth, []string{
				"depression", "depressed", "anxiety", "anxious",
				"panic attack", "mental health", "therapy",
				"eating disorder", "anorexia", "bulimia",
				"bipolar", "schizophren", "ptsd",
			}},
			{SocialMedia, []string{
				"instagram", "tiktok", "snapchat", "twitter",
				"social media", "facebook", "youtube channel",
				"influencer", "follower", "viral", "streaming",
				"twitch", "discord server",
			}},
			{Dating, []string{
				"dating", "boyfriend", "girlfriend", "crush",
				"relationship", "romance", "love letter",
				"tinder", "bumble", "flirting", "kiss",
				"breakup", "break up",
			}},
			{Religion, []string{
				"religion", "religious", "god", "bible", "quran",
				"church", "mosque", "synagogue", "temple",
				"prayer", "worship", "faith", "buddhism",
				"christianity", "islam", "hinduism", "atheism",
			}},
			{Politics, []string{
				"politics", "political", "election", "president",
				"democrat", "republican", "liberal", "conservative",
				"congress", "parliament", "vote", "campaign",
				"immigration", "policy debate", "partisan",
			}},
			{Finance, []string{
				"cryptocurrency", "bitcoin", "ethereum", "crypto",
				"stock market", "investing", "bank account",
				"credit card", "mortgage", "loan", "tax",
				"nft", "blockchain", "trading",
			}},
			{Health, []string{
				"health", "hygiene", "nutrition", "vitamin",
				"exercise", "fitness", "disease", "symptom",
				"doctor", "hospital", "medicine", "vaccine",
				"allergy", "diet", "calories",
			}},
			{Science, []string{
				"science", "experiment", "biology", "chemistry",
				"physics", "atom", "molecule", "dna",
				"evolution", "ecosystem", "planet", "galaxy",
				"telescope", "microscope", "hypothesis",
				"photosynthesis", "gravity", "magnetism",
			}},
			{Math, []string{
				"math", "mathematics", "algebra", "geometry",
				"calculus", "equation", "fraction", "multiply",
				"divide", "percentage", "trigonometry",
				"pythagorean", "logarithm", "derivative",
			}},
			{History, []string{
				"history", "historical", "ancient", "medieval",
				"world war", "revolution", "civilization",
				"empire", "dynasty", "archaeology", "fossil",
				"pharaoh", "roman empire", "renaissance",
			}},
			{Arts, []string{
				"art", "painting", "drawing", "sculpture",
				"music", "piano", "guitar", "singing",
				"craft", "origami", "pottery", "theater",
				"ballet", "dance", "compose",
			}},
			{Sports, []string{
				"sport", "soccer", "football", "basketball",
				"baseball", "tennis", "swimming", "running",
				"olympics", "athlete", "tournament",
				"hockey", "volleyball", "gymnastics",
			}},
			{Technology, []string{
				"programming", "coding", "computer", "software",
				"hardware", "robot", "artificial intelligence",
				"machine learning", "website", "app",
				"javascript", "python", "database", "linux",
			}},
		},
	}
}

// Classify returns the best-matching category for the given text.
// Returns General if no specific category matches.
func (c *Classifier) Classify(text string) Category {
	if text == "" {
		return General
	}

	lower := strings.ToLower(text)

	// Score each category — first match wins in priority order.
	// Rules are ordered by risk: critical > high > medium > low > none.
	for _, r := range c.rules {
		for _, kw := range r.keywords {
			if strings.Contains(lower, kw) {
				return r.category
			}
		}
	}

	return General
}
