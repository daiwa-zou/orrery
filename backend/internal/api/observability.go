package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clusterlens_http_requests_total",
		Help: "HTTP requests by route, method and status.",
	}, []string{"route", "method", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "clusterlens_http_request_duration_seconds",
		Help:    "HTTP request latency by route.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"route", "method"})

	httpInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "clusterlens_http_requests_in_flight",
		Help: "HTTP requests currently being served, streams included.",
	})
)

// statusRecorder captures the status code for metrics without buffering the
// body, and passes Flush through so streaming handlers still stream.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Hijack passes the connection takeover through to the underlying writer.
// Without this the WebSocket upgrader cannot find an http.Hijacker and every
// stream fails its handshake — with no server-side error to explain why,
// since the upgrader answers the client directly.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("clusterlens: %T does not support hijacking", s.ResponseWriter)
	}
	return h.Hijack()
}

// observe records request metrics. Routes are labelled by their chi pattern,
// never by the concrete path, so cardinality stays bounded no matter how many
// namespaces or object names exist.
func (a *API) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		httpInFlight.Inc()
		defer httpInFlight.Dec()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := "unmatched"
		if ctx := chi.RouteContext(r.Context()); ctx != nil {
			if p := ctx.RoutePattern(); p != "" {
				route = p
			}
		}
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
		httpDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
	})
}

// cacheCollector reports live informer statistics as Prometheus gauges. It
// collects on scrape rather than maintaining counters, so it costs nothing
// between scrapes.
type cacheCollector struct {
	api      *API
	objects  *prometheus.Desc
	watchers *prometheus.Desc
	count    *prometheus.Desc
}

// NewCacheCollector builds the collector for the registry.
func NewCacheCollector(a *API) prometheus.Collector {
	return &cacheCollector{
		api: a,
		objects: prometheus.NewDesc("clusterlens_cache_objects",
			"Objects held in a resource cache.", []string{"cluster", "gvr"}, nil),
		watchers: prometheus.NewDesc("clusterlens_cache_subscribers",
			"WebSocket subscribers attached to a resource cache.", []string{"cluster", "gvr"}, nil),
		count: prometheus.NewDesc("clusterlens_cache_informers",
			"Running informers per cluster.", []string{"cluster"}, nil),
	}
}

func (c *cacheCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.objects
	ch <- c.watchers
	ch <- c.count
}

func (c *cacheCollector) Collect(ch chan<- prometheus.Metric) {
	for _, entry := range c.api.registry.Entries() {
		if entry.Cluster == nil {
			continue
		}
		stats := entry.Cluster.Informers.Stats()
		ch <- prometheus.MustNewConstMetric(c.count, prometheus.GaugeValue, float64(len(stats)), entry.Name)
		for _, s := range stats {
			ch <- prometheus.MustNewConstMetric(c.objects, prometheus.GaugeValue, float64(s.Objects), entry.Name, s.GVR)
			ch <- prometheus.MustNewConstMetric(c.watchers, prometheus.GaugeValue, float64(s.Subscribers), entry.Name, s.GVR)
		}
	}
}
