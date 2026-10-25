// Package metrics exposes Prometheus metrics at /metrics.
package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

type Metrics struct {
	reg          *prometheus.Registry
	runDuration  *prometheus.HistogramVec
	runnerExits  *prometheus.CounterVec
	telegramErrs *prometheus.CounterVec
	githubErrs   *prometheus.CounterVec
}

func New(st *store.Store) *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		runDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "fleetpocket_run_duration_seconds",
			Help:    "Wall time of runner containers.",
			Buckets: []float64{30, 60, 120, 300, 600, 1200, 1800, 3600, 7200},
		}, []string{"project"}),
		runnerExits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fleetpocket_runner_exits_total", Help: "Runner exits by exit code.",
		}, []string{"code"}),
		telegramErrs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fleetpocket_telegram_api_errors_total", Help: "Failed Telegram API calls.",
		}, []string{"op"}),
		githubErrs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fleetpocket_github_api_errors_total", Help: "Failed GitHub API calls.",
		}, []string{"op"}),
	}
	m.reg.MustRegister(m.runDuration, m.runnerExits, m.telegramErrs, m.githubErrs,
		&dbCollector{st: st},
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

func (m *Metrics) RunFinished(project string, exitCode int, d time.Duration) {
	m.runDuration.WithLabelValues(project).Observe(d.Seconds())
	m.runnerExits.WithLabelValues(strconv.Itoa(exitCode)).Inc()
}

func (m *Metrics) TelegramError(op string) { m.telegramErrs.WithLabelValues(op).Inc() }
func (m *Metrics) GitHubError(op string)   { m.githubErrs.WithLabelValues(op).Inc() }

// dbCollector reads task counts and costs at scrape time.
type dbCollector struct{ st *store.Store }

var (
	tasksDesc = prometheus.NewDesc("fleetpocket_tasks", "Tasks by status.", []string{"status"}, nil)
	queueDesc = prometheus.NewDesc("fleetpocket_queue_length", "Tasks waiting for a slot.", nil, nil)
	costDesc  = prometheus.NewDesc("fleetpocket_cost_usd_total", "Total Claude cost by project.", []string{"project"}, nil)
)

func (c *dbCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- tasksDesc
	ch <- queueDesc
	ch <- costDesc
}

func (c *dbCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	counts, err := c.st.CountByStatus(ctx)
	if err != nil {
		slog.Warn("metrics: count tasks", "err", err)
		return
	}
	queued := 0
	for _, s := range counts {
		ch <- prometheus.MustNewConstMetric(tasksDesc, prometheus.GaugeValue, float64(s.Count), s.Status)
		if s.Status == "queued" {
			queued = s.Count
		}
	}
	ch <- prometheus.MustNewConstMetric(queueDesc, prometheus.GaugeValue, float64(queued))
	costs, err := c.st.CostByProject(ctx)
	if err != nil {
		slog.Warn("metrics: cost", "err", err)
		return
	}
	for _, pc := range costs {
		ch <- prometheus.MustNewConstMetric(costDesc, prometheus.CounterValue, pc.Cost, pc.ProjectID)
	}
}
