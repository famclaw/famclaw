package classifier

// Category represents a topic category for policy evaluation.
type Category string

const (
	General         Category = "general"
	Science         Category = "science"
	Math            Category = "math"
	History         Category = "history"
	Arts            Category = "arts"
	Sports          Category = "sports"
	Technology      Category = "technology"
	Health          Category = "health"
	SocialMedia     Category = "social_media"
	Dating          Category = "dating"
	Religion        Category = "religion"
	Politics        Category = "politics"
	Finance         Category = "finance"
	MentalHealth    Category = "mental_health"
	Violence        Category = "violence"
	Drugs           Category = "drugs"
	SexualContent   Category = "sexual_content"
	Gambling        Category = "gambling"
	Hacking         Category = "hacking"
	SelfHarm        Category = "self_harm"
	HateSpeech      Category = "hate_speech"
	IllegalActivity Category = "illegal_activity"
)
