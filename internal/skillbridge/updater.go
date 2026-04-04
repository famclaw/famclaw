package skillbridge

import (
	"context"
	"log"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// Updater periodically checks installed skills for updates.
type Updater struct {
	db       *store.DB
	interval time.Duration
}

// NewUpdater creates an update checker that runs every interval.
func NewUpdater(db *store.DB, interval time.Duration) *Updater {
	return &Updater{db: db, interval: interval}
}

// Run starts the update checker loop. Blocks until ctx is cancelled.
func (u *Updater) Run(ctx context.Context) {
	// Check once on startup
	u.checkAll(ctx)

	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.checkAll(ctx)
		}
	}
}

func (u *Updater) checkAll(ctx context.Context) {
	skills, err := u.db.ListInstalledSkills()
	if err != nil {
		log.Printf("[updater] error listing skills: %v", err)
		return
	}
	for _, skill := range skills {
		if skill.Disabled {
			continue
		}
		u.checkOne(ctx, skill)
	}
}

func (u *Updater) checkOne(ctx context.Context, skill *store.InstalledSkill) {
	// TODO: query GitHub/GitLab releases API for latest version
	// For now, log that we checked
	log.Printf("[updater] checked %s v%s — no update mechanism yet", skill.Name, skill.Version)
	u.db.LogUpdateCheck(skill.Name, skill.Version, "", "SKIP", "update mechanism not implemented", false)
}
