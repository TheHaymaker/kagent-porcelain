package api

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/usage"
)

// GetUsageSummary implements UsageService.
func (s *Server) GetUsageSummary(ctx context.Context, req *wb.GetUsageSummaryRequest) (*wb.GetUsageSummaryResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	since, until := usage.MonthStart(timeNow()), timeNow().Add(time.Minute)
	if req.GetSince() != nil {
		since = req.GetSince().AsTime()
	}
	if req.GetUntil() != nil {
		until = req.GetUntil().AsTime()
	}
	person := ""
	if req.GetMine() || !s.has(c, authz.PlatformEngineer) {
		person = c.ID
	}
	group := map[wb.UsageGroupBy]store.UsageGroup{wb.UsageGroupBy_USAGE_GROUP_BY_PERSON: store.ByPerson,
		wb.UsageGroupBy_USAGE_GROUP_BY_APP: store.ByApp, wb.UsageGroupBy_USAGE_GROUP_BY_MODEL: store.ByModel}[req.GetGroupBy()]
	if group == "" {
		group = store.ByDay
	}
	lines, err := s.Store.SummariseUsage(ctx, since, until, group, person)
	if err != nil {
		return nil, toStatus(err)
	}
	out := &wb.GetUsageSummaryResponse{}
	for _, l := range lines {
		out.Lines = append(out.Lines, &wb.UsageLine{Key: l.Key, InputTokens: l.InputTokens, OutputTokens: l.OutputTokens, CostCents: l.CostMicros / 10_000})
		out.TotalCostCents += l.CostMicros / 10_000
	}
	return out, nil
}

// ListLimits implements UsageService. People see the limits that apply to
// them; platform engineers see all.
func (s *Server) ListLimits(ctx context.Context, _ *wb.ListLimitsRequest) (*wb.ListLimitsResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	limits, err := s.Store.Limits(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	platform := s.has(c, authz.PlatformEngineer)
	since := usage.MonthStart(timeNow())
	out := &wb.ListLimitsResponse{}
	for _, l := range limits {
		mine := (l.Subject == "person" && l.SubjectID == c.ID) || (l.Subject == "team" && contains(c.Groups, l.SubjectID))
		if !platform && !mine {
			continue
		}
		members := []string{l.SubjectID}
		if l.Subject == "team" {
			people, _ := s.Store.PeopleInGroup(ctx, l.SubjectID)
			members = members[:0]
			for _, p := range people {
				members = append(members, p.ID)
			}
		}
		spent, _ := s.Store.SpentMicros(ctx, members, since)
		subject := wb.LimitSubject_LIMIT_SUBJECT_PERSON
		if l.Subject == "team" {
			subject = wb.LimitSubject_LIMIT_SUBJECT_TEAM
		}
		out.Limits = append(out.Limits, &wb.Limit{Subject: subject, SubjectId: l.SubjectID, MonthlyCents: l.MonthlyCents, SpentCentsMtd: spent / 10_000})
	}
	return out, nil
}

// SetLimit implements UsageService. Only platform engineers set limits.
func (s *Server) SetLimit(ctx context.Context, req *wb.SetLimitRequest) (*wb.SetLimitResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if !s.has(c, authz.PlatformEngineer) {
		return nil, status.Error(codes.PermissionDenied, "only platform engineers set limits")
	}
	l := req.GetLimit()
	subject := map[wb.LimitSubject]string{wb.LimitSubject_LIMIT_SUBJECT_PERSON: "person", wb.LimitSubject_LIMIT_SUBJECT_TEAM: "team"}[l.GetSubject()]
	if subject == "" || l.GetSubjectId() == "" || l.GetMonthlyCents() < 0 {
		return nil, status.Error(codes.InvalidArgument, "a limit needs a subject, an id and a non-negative amount")
	}
	if err := s.Store.SetLimit(ctx, store.Limit{Subject: subject, SubjectID: l.GetSubjectId(), MonthlyCents: l.GetMonthlyCents()}); err != nil {
		return nil, toStatus(err)
	}
	s.Audit.Record(ctx, store.AuditEvent{Type: audit.LimitSet, ActorID: c.ID, Surface: c.Surface, Summary: "Set a monthly limit for " + subject + " " + l.GetSubjectId(),
		Detail: map[string]any{"monthlyCents": l.GetMonthlyCents()}})
	return &wb.SetLimitResponse{Limit: l}, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
