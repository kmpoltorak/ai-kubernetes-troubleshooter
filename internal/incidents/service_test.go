package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

type fakeRepo struct {
	incidents map[string]domain.Incident
	gotLimit  int
}

func (f *fakeRepo) CreateIncident(_ context.Context, in *domain.Incident) error {
	in.ID = domain.NewID()
	in.Status = domain.IncidentOpen
	f.incidents[in.ID] = *in
	return nil
}

func (f *fakeRepo) GetIncident(_ context.Context, id string) (domain.Incident, error) {
	if inc, ok := f.incidents[id]; ok {
		return inc, nil
	}
	return domain.Incident{}, domain.ErrNotFound
}

func (f *fakeRepo) ListIncidents(_ context.Context, limit, _ int) ([]domain.Incident, error) {
	f.gotLimit = limit
	return nil, nil
}

func (f *fakeRepo) LatestReport(context.Context, string) (domain.InvestigationRecord, error) {
	return domain.InvestigationRecord{}, domain.ErrNotFound
}

func (f *fakeRepo) ListInvestigations(context.Context, string, int, int) ([]domain.Investigation, error) {
	return nil, nil
}
func (f *fakeRepo) GetInvestigation(context.Context, string, string) (domain.InvestigationRecord, error) {
	return domain.InvestigationRecord{}, domain.ErrNotFound
}

func newService() (*Service, *fakeRepo) {
	repo := &fakeRepo{incidents: map[string]domain.Incident{}}
	return NewService(repo, domain.TargetPolicy{ClusterName: "local", AllowedNamespaces: []string{"payments", "orders"}}), repo
}

func valid() CreateInput {
	return CreateInput{Description: "Pods are repeatedly restarting", Cluster: "local", Namespace: "payments",
		ResourceType: "deployment", ResourceName: "payment-service"}
}

func TestCreateNormalizesAndStores(t *testing.T) {
	svc, repo := newService()
	in := valid()
	in.ResourceType, in.Namespace = " Deployment ", " payments "
	inc, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if inc.Title != "deployment/payment-service in payments" || inc.ResourceType != domain.ResourceDeployment ||
		inc.Namespace != "payments" || repo.incidents[inc.ID].ID == "" {
		t.Fatalf("unexpected incident: %+v", inc)
	}
}

func TestCreateValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateInput)
		field  string
		msg    string
	}{
		{"missing description", func(in *CreateInput) { in.Description = " " }, "description", "required"},
		{"long description", func(in *CreateInput) { in.Description = strings.Repeat("x", 5001) }, "description", "5000"},
		{"long title", func(in *CreateInput) { in.Title = strings.Repeat("x", 201) }, "title", "200"},
		{"bad resource type", func(in *CreateInput) { in.ResourceType = "secret" }, "target", "resource_type"},
		{"injection name", func(in *CreateInput) { in.ResourceName = "api; kubectl delete ns payments" }, "target", "resource_name"},
		{"bad namespace", func(in *CreateInput) { in.Namespace = "Payments" }, "target", "namespace"},
		{"unknown cluster", func(in *CreateInput) { in.Cluster = "prod-eu" }, "target", "not configured"},
		{"namespace not allowed", func(in *CreateInput) { in.Namespace = "kube-system" }, "target", "allowed namespaces"},
	}
	svc, _ := newService()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid()
			tt.mutate(&in)
			_, err := svc.Create(context.Background(), in)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tt.field || !strings.Contains(ve.Message, tt.msg) {
				t.Fatalf("want validation error on %s containing %q, got %v", tt.field, tt.msg, err)
			}
		})
	}
}

func TestListClampsLimit(t *testing.T) {
	svc, repo := newService()
	for _, tc := range []struct{ in, want int }{{0, DefaultPageSize}, {-5, DefaultPageSize}, {50, 50}, {1000, MaxPageSize}} {
		if _, err := svc.List(context.Background(), tc.in, 0); err != nil {
			t.Fatal(err)
		}
		if repo.gotLimit != tc.want {
			t.Errorf("limit %d -> %d, want %d", tc.in, repo.gotLimit, tc.want)
		}
	}
	if _, err := svc.List(context.Background(), 10, -1); err == nil {
		t.Fatal("negative offset accepted")
	}
}

func TestReportNotFound(t *testing.T) {
	svc, _ := newService()
	_, err := svc.Report(context.Background(), domain.NewID())
	if !errors.Is(err, domain.ErrNotFound) || !strings.Contains(err.Error(), "incident") {
		t.Fatalf("got %v", err)
	}
	inc, _ := svc.Create(context.Background(), valid())
	_, err = svc.Report(context.Background(), inc.ID)
	if !errors.Is(err, domain.ErrNotFound) || !strings.Contains(err.Error(), "completed investigation") {
		t.Fatalf("got %v", err)
	}
}

func TestCreateMasksBeforeStorageAndGetMasksLegacy(t *testing.T) {
	svc, repo := newService()
	in := valid()
	in.Title = "password=synthetic-title"
	in.Description = "password=synthetic-description"
	inc, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	stored := repo.incidents[inc.ID]
	if strings.Contains(inc.Title+inc.Description+stored.Title+stored.Description, "synthetic-") {
		t.Fatalf("leaked: %+v", stored)
	}
	stored.Description = "password=legacy-secret"
	repo.incidents[inc.ID] = stored
	got, err := svc.Get(context.Background(), inc.ID)
	if err != nil || strings.Contains(got.Description, "legacy-secret") || repo.incidents[inc.ID].Description != stored.Description {
		t.Fatalf("unsafe legacy read: %+v %v", got, err)
	}
	in.Description = "password=" + strings.Repeat("x", 5001)
	if _, err := svc.Create(context.Background(), in); err == nil {
		t.Fatal("masking bypassed length validation")
	}
}

type legacyRepo struct {
	fakeRepo
	list   []domain.Incident
	record domain.InvestigationRecord
}

func (r *legacyRepo) ListIncidents(context.Context, int, int) ([]domain.Incident, error) {
	return r.list, nil
}
func (r *legacyRepo) LatestReport(context.Context, string) (domain.InvestigationRecord, error) {
	return r.record, nil
}

func TestLegacyListAndReportDoNotMutateStorage(t *testing.T) {
	inc := domain.Incident{ID: domain.NewID(), Description: "password=synthetic-legacy"}
	repo := &legacyRepo{fakeRepo: fakeRepo{incidents: map[string]domain.Incident{inc.ID: inc}}, list: []domain.Incident{inc}, record: domain.InvestigationRecord{Evidence: []domain.Evidence{{Summary: inc.Description, Data: json.RawMessage(`{"password":"synthetic-data"}`)}}}}
	svc := NewService(repo, domain.TargetPolicy{})
	list, err := svc.List(context.Background(), 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := svc.Report(context.Background(), inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rec)
	if strings.Contains(list[0].Description+string(raw), "synthetic-") || repo.list[0].Description != inc.Description || repo.record.Evidence[0].Summary != inc.Description {
		t.Fatal("legacy masking leaked or mutated source")
	}
	repo.record.Evidence[0].Data = json.RawMessage(`{"password":"synthetic-invalid"`)
	if _, err := svc.Report(context.Background(), inc.ID); err == nil || strings.Contains(err.Error(), "synthetic-invalid") {
		t.Fatalf("unsafe malformed record: %v", err)
	}
}
