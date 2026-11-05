// Package incidents implements incident creation, retrieval and listing.
package incidents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

type Repository interface {
	CreateIncident(ctx context.Context, in *domain.Incident) error
	GetIncident(ctx context.Context, id string) (domain.Incident, error)
	ListIncidents(ctx context.Context, limit, offset int) ([]domain.Incident, error)
	LatestReport(ctx context.Context, incidentID string) (domain.InvestigationRecord, error)
	ListInvestigations(ctx context.Context, incidentID string, limit, offset int) ([]domain.Investigation, error)
	GetInvestigation(ctx context.Context, incidentID, id string) (domain.InvestigationRecord, error)
	DeleteIncidents(ctx context.Context, ids []string) ([]string, error)
}

// ValidationError describes invalid user input; the API maps it to 400.
type ValidationError struct {
	Field, Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

type Service struct {
	repo   Repository
	policy domain.TargetPolicy
}

func NewService(repo Repository, policy domain.TargetPolicy) *Service {
	return &Service{repo: repo, policy: policy}
}

type CreateInput struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	Cluster      string `json:"cluster"`
	Namespace    string `json:"namespace"`
	ResourceType string `json:"resource_type"`
	ResourceName string `json:"resource_name"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (domain.Incident, error) {
	inc := domain.Incident{
		Title:       strings.TrimSpace(in.Title),
		Description: strings.TrimSpace(in.Description),
		Target: domain.Target{
			Cluster:      strings.TrimSpace(in.Cluster),
			Namespace:    strings.TrimSpace(in.Namespace),
			ResourceType: domain.ResourceType(strings.ToLower(strings.TrimSpace(in.ResourceType))),
			ResourceName: strings.TrimSpace(in.ResourceName),
		},
	}
	if inc.Title == "" {
		inc.Title = fmt.Sprintf("%s in %s", inc.Ref(), inc.Namespace)
	}
	if err := s.validate(inc); err != nil {
		return domain.Incident{}, err
	}
	inc = inc.Redacted()
	if err := s.repo.CreateIncident(ctx, &inc); err != nil {
		return domain.Incident{}, fmt.Errorf("create incident: %w", err)
	}
	observability.IncidentsTotal.Inc()
	return inc, nil
}

func (s *Service) validate(inc domain.Incident) error {
	switch {
	case inc.Description == "":
		return &ValidationError{"description", "is required"}
	case utf8.RuneCountInString(inc.Description) > 5000:
		return &ValidationError{"description", "must be at most 5000 characters"}
	case utf8.RuneCountInString(inc.Title) > 200:
		return &ValidationError{"title", "must be at most 200 characters"}
	}
	if err := inc.Validate(); err != nil {
		return &ValidationError{"target", err.Error()}
	}
	if err := s.policy.Check(inc.Target); err != nil {
		return &ValidationError{"target", err.Error()}
	}
	return nil
}

func (s *Service) Get(ctx context.Context, id string) (domain.Incident, error) {
	inc, err := s.repo.GetIncident(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return inc, fmt.Errorf("incident %w", err)
	}
	return inc.Redacted(), err
}

// List returns a page of incidents, newest first. A non-positive limit
// selects the default page size; larger limits are capped.
func (s *Service) List(ctx context.Context, limit, offset int) ([]domain.Incident, error) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	limit = min(limit, MaxPageSize)
	if offset < 0 {
		return nil, &ValidationError{"offset", "must not be negative"}
	}
	list, err := s.repo.ListIncidents(ctx, limit, offset)
	list = slices.Clone(list)
	for i := range list {
		list[i] = list[i].Redacted()
	}
	return list, err
}

// MaxDelete bounds one delete request.
const MaxDelete = 100

// Delete removes incidents with all their investigations. Incidents with a
// queued or running investigation, and unknown IDs, are skipped; the result
// says which were deleted.
func (s *Service) Delete(ctx context.Context, ids []string) (deleted, skipped []string, err error) {
	if len(ids) == 0 || len(ids) > MaxDelete {
		return nil, nil, &ValidationError{"ids", fmt.Sprintf("must list 1 to %d incident IDs", MaxDelete)}
	}
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		if !domain.IsUUID(id) {
			return nil, nil, &ValidationError{"ids", fmt.Sprintf("%q is not a UUID", id)}
		}
		normalized = append(normalized, strings.ToLower(id)) // PostgreSQL returns UUIDs lowercase
	}
	ids = slices.Compact(slices.Sorted(slices.Values(normalized)))
	deleted, err = s.repo.DeleteIncidents(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	deleted = append([]string{}, deleted...)
	skipped = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return slices.Contains(deleted, id) })
	return deleted, skipped, nil
}

// Report returns the latest completed investigation of an incident.
func (s *Service) Report(ctx context.Context, incidentID string) (domain.InvestigationRecord, error) {
	if _, err := s.Get(ctx, incidentID); err != nil {
		return domain.InvestigationRecord{}, err
	}
	rec, err := s.repo.LatestReport(ctx, incidentID)
	if errors.Is(err, domain.ErrNotFound) {
		return rec, fmt.Errorf("completed investigation %w", err)
	}
	if err != nil {
		return domain.InvestigationRecord{}, err
	}
	return rec.Redacted()
}

// InvestigationPage is a bounded page of attempt headers, including failures.
type InvestigationPage struct {
	Investigations []domain.Investigation `json:"investigations"`
	Offset         int                    `json:"offset"`
	Limit          int                    `json:"limit"`
	HasMore        bool                   `json:"has_more"`
}

func (s *Service) Investigations(ctx context.Context, incidentID string, limit, offset int) (InvestigationPage, error) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	limit = min(limit, MaxPageSize)
	if offset < 0 {
		return InvestigationPage{}, &ValidationError{"offset", "must not be negative"}
	}
	if _, err := s.Get(ctx, incidentID); err != nil {
		return InvestigationPage{}, err
	}
	list, err := s.repo.ListInvestigations(ctx, incidentID, limit+1, offset)
	if err != nil {
		return InvestigationPage{}, err
	}
	more := len(list) > limit
	list = append([]domain.Investigation{}, list[:min(len(list), limit)]...)
	for i := range list {
		list[i] = list[i].Redacted()
	}
	return InvestigationPage{Investigations: list, Offset: offset, Limit: limit, HasMore: more}, nil
}

func (s *Service) Investigation(ctx context.Context, incidentID, id string) (domain.InvestigationRecord, error) {
	rec, err := s.repo.GetInvestigation(ctx, incidentID, id)
	if err != nil {
		return domain.InvestigationRecord{}, err
	}
	return rec.Redacted()
}
