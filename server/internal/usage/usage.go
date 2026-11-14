// Package usage meters spend and enforces limits. Usage arrives from the
// model gateway, which sees every model call whatever the agent runtime, so
// Claude Code and Codex are metered as accurately as kagent's own runtimes.
package usage

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// Notifier is notify.Notifier's sending half.
type Notifier interface {
	Notify(ctx context.Context, personIDs []string, n notify.Notice)
}

// Meter records usage and checks limits.
type Meter struct {
	Store  *store.Store
	Notify Notifier
	Now    func() time.Time
}

func (m *Meter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// MonthStart is the start of the current calendar month in UTC.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Record stores one call's usage and warns the person if it took them over
// a limit.
func (m *Meter) Record(ctx context.Context, u store.UsageRecord) error {
	if u.PersonID == "" || u.Model == "" {
		return fmt.Errorf("usage needs a person and a model")
	}
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CostMicros < 0 {
		return fmt.Errorf("usage cannot be negative")
	}
	if err := m.Store.RecordUsage(ctx, u); err != nil {
		return err
	}
	status, err := m.Check(ctx, u.PersonID, nil)
	if err == nil && status.Over && m.Notify != nil {
		m.Notify.Notify(ctx, []string{u.PersonID}, notify.Notice{Kind: notify.LimitReached, Title: "You have reached your spend limit",
			Body: fmt.Sprintf("You have spent $%.2f of $%.2f this month.", float64(status.SpentCents)/100, float64(status.LimitCents)/100),
			Link: "/usage", DedupeKey: "limit:" + MonthStart(m.now()).Format("2006-01")})
	}
	return nil
}

// Status is where a person stands against their tightest limit.
type Status struct {
	Over       bool
	SpentCents int64
	LimitCents int64
	Subject    string
}

// Check returns whether a person is over any limit that applies to them:
// their own, or one for a team (directory group) they belong to, measured
// across everyone in that team.
func (m *Meter) Check(ctx context.Context, personID string, groups []string) (Status, error) {
	limits, err := m.Store.Limits(ctx)
	if err != nil {
		return Status{}, err
	}
	if groups == nil {
		if p, err := m.Store.GetPerson(ctx, personID); err == nil {
			groups = p.Groups
		}
	}
	since := MonthStart(m.now())
	var worst Status
	for _, l := range limits {
		var members []string
		switch {
		case l.Subject == "person" && l.SubjectID == personID:
			members = []string{personID}
		case l.Subject == "team" && slices.Contains(groups, l.SubjectID):
			people, err := m.Store.PeopleInGroup(ctx, l.SubjectID)
			if err != nil {
				return Status{}, err
			}
			for _, p := range people {
				members = append(members, p.ID)
			}
		default:
			continue
		}
		spent, err := m.Store.SpentMicros(ctx, members, since)
		if err != nil {
			return Status{}, err
		}
		s := Status{SpentCents: spent / 10_000, LimitCents: l.MonthlyCents, Subject: l.Subject + ":" + l.SubjectID}
		s.Over = s.SpentCents >= s.LimitCents
		// Report the limit with the least headroom left.
		if worst.Subject == "" || s.LimitCents-s.SpentCents < worst.LimitCents-worst.SpentCents {
			worst = s
		}
	}
	return worst, nil
}
