# API Server Skeleton with Echo

Boilerplate HTTP API server with a few features:

* use [echo](github.com/labstack/echo/v4) framework for API handlers
* on a separate port, serve prometheus /metrics and /debug/pprof/*
* listen for SIGINT,SIGTERM and shutdown gracefully
* setup slog, maybe --verbose, maybe json formatted