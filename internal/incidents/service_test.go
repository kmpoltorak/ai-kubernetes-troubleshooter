package incidents

import (
	"context"
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
		{"bad resource type", func(in *CreateInput) { in.ResourceType = "statefulset" }, "target", "resource_type"},
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
