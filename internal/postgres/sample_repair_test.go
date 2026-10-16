package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/history"
	promsource "github.com/canberkturan/keda-replica-sense/internal/prometheus"
	"github.com/canberkturan/keda-replica-sense/internal/sampler"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

// Called only within the transaction test's private disposable schema.
func testTrainingHistoryRepair(t *testing.T, repo *SampleRepository, w workloadstore.SamplingWorkload) {
	t.Helper()
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Minute).Add(-time.Hour)
	end := start.Add(time.Hour)
	w.Spec.Forecast.TrainingWindow = time.Hour
	var original []domain.Sample
	for i := 0; i < 60; i++ {
		if i == 0 || i == 1 || i == 30 || i == 59 {
			continue
		}
		original = append(original, sampler.BuildSample(w, start.Add(time.Duration(i)*time.Minute+5*time.Second), 17))
	}
	if err := repo.UpsertSamples(ctx, original); err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	available.Store(true)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		count := requests.Add(1)
		if request.URL.Path != "/api/v1/query_range" || request.URL.Query().Get("query") != "metric" {
			t.Error("wrong source query")
		}
		from, err := time.Parse(time.RFC3339, request.URL.Query().Get("start"))
		if err != nil {
			t.Error(err)
		}
		to, err := time.Parse(time.RFC3339, request.URL.Query().Get("end"))
		if err != nil {
			t.Error(err)
		}
		// A sampler observation arriving during HTTP I/O must win over repair.
		if count == 1 {
			if err := repo.UpsertSamples(ctx, []domain.Sample{sampler.BuildSample(w, start.Add(30*time.Minute+5*time.Second), 999)}); err != nil {
				t.Error(err)
			}
		}
		values := make([][]any, 0)
		if available.Load() {
			for at := from; !at.After(to); at = at.Add(time.Minute) {
				values = append(values, []any{at.Unix(), "123"})
			}
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{"values": values}}}})
	}))
	defer server.Close()
	w.Spec.Source.ServerAddress = server.URL
	w.Spec.Source.Query = "metric"
	repairer := history.Repairer{Store: repo, Querier: promsource.Client{HTTPClient: server.Client()}, Policy: history.DefaultPolicy()}
	report, err := repairer.Repair(ctx, w, start, end)
	if err != nil || report.MissingBefore != 4 || report.Inserted != 3 || report.MissingAfter != 0 {
		t.Fatalf("HTTP/PostgreSQL repair: %+v %v", report, err)
	}
	values, err := repo.ListSamplesForWindow(ctx, w, start, end)
	if err != nil || len(values) != 60 {
		t.Fatal("missing or duplicate rows", len(values), err)
	}
	for _, p := range values {
		if p.ObservedAt.Second() == 5 && p.ObservedValue != 17 && p.ObservedValue != 999 {
			t.Fatal("real observation overwritten")
		}
	}
	report, err = repairer.Repair(ctx, w, start, end)
	if err != nil || report.Requests != 0 {
		t.Fatal("unnecessary source query", report, err)
	}
	if _, err := repo.database.Exec(ctx, `DELETE FROM samples WHERE workload_id=$1 AND observed_at=$2`, w.ID, start); err != nil {
		t.Fatal(err)
	}
	available.Store(false)
	if report, err := repairer.Repair(ctx, w, start, end); err == nil || report.MissingAfter != 1 {
		t.Fatal("unavailable source fabricated a sample", report, err)
	}
	available.Store(true)
	if _, err := repo.database.Exec(ctx, `UPDATE workloads SET policy_revision=$2 WHERE id=$1`, w.ID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := repairer.Repair(ctx, w, start, end); err == nil {
		t.Fatal("stale policy wrote repaired data")
	}
	if _, err := repo.database.Exec(ctx, `UPDATE workloads SET policy_revision=$2 WHERE id=$1`, w.ID, w.Spec.PolicyRevision); err != nil {
		t.Fatal(err)
	}
}
