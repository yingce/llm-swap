package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestVictoriaMetricsClientQueryRange(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotStart string
	var gotEnd string
	var gotStep string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		gotStart = r.URL.Query().Get("start")
		gotEnd = r.URL.Query().Get("end")
		gotStep = r.URL.Query().Get("step")
		writeJSON(w, map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []any{
					map[string]any{
						"metric": map[string]string{"model": "qwen", "__name__": "llm_swap_gateway_requests_total"},
						"values": [][]any{
							{float64(1710000000), "2"},
							{float64(1710000060), "4.5"},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewVictoriaMetricsClient(server.URL, time.Second)
	start := time.Unix(1710000000, 0)
	end := time.Unix(1710000060, 0)
	series, err := client.QueryRange(context.Background(), "requests", `sum(rate(llm_swap_gateway_requests_total{model="qwen"}[1m]))`, start, end, time.Minute)
	if err != nil {
		t.Fatalf("QueryRange returned error: %v", err)
	}

	if gotPath != "/prometheus/api/v1/query_range" {
		t.Fatalf("path = %q, want VictoriaMetrics query_range path", gotPath)
	}
	if gotQuery != `sum(rate(llm_swap_gateway_requests_total{model="qwen"}[1m]))` {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotStart != strconv.FormatFloat(float64(start.Unix()), 'f', -1, 64) || gotEnd != strconv.FormatFloat(float64(end.Unix()), 'f', -1, 64) || gotStep != "60" {
		t.Fatalf("range params start=%q end=%q step=%q", gotStart, gotEnd, gotStep)
	}
	if len(series) != 1 {
		t.Fatalf("series len = %d, want 1", len(series))
	}
	if series[0].Name != "requests" || series[0].Labels["model"] != "qwen" {
		t.Fatalf("series metadata = %+v", series[0])
	}
	if len(series[0].Points) != 2 || series[0].Points[1].Value != 4.5 {
		t.Fatalf("series points = %+v", series[0].Points)
	}
}

func TestParseMetricsRangeClampsRangeAndStep(t *testing.T) {
	now := time.Unix(1710003600, 0)
	start, end, step, label := parseMetricsRange("14d", "5m", "1h", "7d", now)

	if !end.Equal(now) {
		t.Fatalf("end = %v, want now", end)
	}
	if got := end.Sub(start); got != 7*24*time.Hour {
		t.Fatalf("range = %v, want 7d", got)
	}
	if step != 5*time.Minute {
		t.Fatalf("step = %v, want 5m", step)
	}
	if label != "7d" {
		t.Fatalf("label = %q, want clamped 7d", label)
	}
}

func TestParseMetricsRangeDefaultsInvalidValues(t *testing.T) {
	now := time.Unix(1710003600, 0)
	start, _, step, label := parseMetricsRange("bad", "bad", "2h", "24h", now)

	if got := now.Sub(start); got != 2*time.Hour {
		t.Fatalf("range = %v, want default 2h", got)
	}
	if step <= 0 {
		t.Fatalf("step = %v, want positive default", step)
	}
	if label != "2h" {
		t.Fatalf("label = %q, want 2h", label)
	}
}
