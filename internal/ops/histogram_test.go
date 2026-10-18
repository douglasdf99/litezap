package ops

import (
	"strings"
	"testing"
)

func TestHistogramAndWorkerMemoryMetrics(t *testing.T) {
	hist := NewHistogram([]float64{1, 10})
	for _, v := range []float64{0.5, 1, 3, 100} {
		hist.Observe(v)
	}
	var nilHist *Histogram
	nilHist.Observe(1) // no panic
	src := Source{SendLatency: hist, WorkerMemory: func() map[string]int64 { return map[string]int64{"inst_b": 2048, "inst_a": 1024} }}
	code, body := get(t, Handler(&Health{}, Metrics(src)), "/metrics")
	if code != 200 {
		t.Fatalf("metrics = %d", code)
	}
	for _, want := range []string{
		"# TYPE litezap_queue_send_latency_seconds histogram",
		`litezap_queue_send_latency_seconds_bucket{le="1"} 2`,
		`litezap_queue_send_latency_seconds_bucket{le="10"} 3`,
		`litezap_queue_send_latency_seconds_bucket{le="+Inf"} 4`,
		"litezap_queue_send_latency_seconds_sum 104.5",
		"litezap_queue_send_latency_seconds_count 4",
		"# TYPE litezap_worker_memory_rss_bytes gauge",
		`litezap_worker_memory_rss_bytes{instance_id="inst_a"} 1024`,
		`litezap_worker_memory_rss_bytes{instance_id="inst_b"} 2048`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Index(body, `instance_id="inst_a"`) > strings.Index(body, `instance_id="inst_b"`) {
		t.Error("worker samples not sorted")
	}
}
