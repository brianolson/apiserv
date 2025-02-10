package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// TODO: more metrics here

var reqDur = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "http_request_duration_seconds",
	Help:    "A histogram of latencies for requests.",
	Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
}, []string{"code", "method", "path"})

var reqCnt = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "http_requests_total",
	Help: "A counter for requests to the wrapped handler.",
}, []string{"code", "method", "path"})

func MetricsMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Path()
		if path == "/metrics" || path == "/_health" {
			return next(c)
		}

		start := time.Now()

		err := next(c)

		status := c.Response().Status
		if err != nil {
			var httpError *echo.HTTPError
			if errors.As(err, &httpError) {
				status = httpError.Code
			}
			if status == 0 || status == http.StatusOK {
				status = http.StatusInternalServerError
			}
		}

		elapsed := float64(time.Since(start)) / float64(time.Second)

		statusStr := strconv.Itoa(status)
		method := c.Request().Method

		reqDur.WithLabelValues(statusStr, method, path).Observe(elapsed)
		reqCnt.WithLabelValues(statusStr, method, path).Inc()

		return err
	}
}
