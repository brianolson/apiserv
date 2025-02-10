package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v3"
)

func main() {
	app := cli.Command{
		Name:  "didserv",
		Flags: []cli.Flag{},
		Commands: []*cli.Command{
			serveCmd,
		},
	}

	err := app.Run(context.Background(), os.Args)
	if err != nil {
		slog.Error("didserv", "err", err)
		fmt.Fprintf(os.Stderr, "%s\n", err.Error())
		os.Exit(1)
	}
}

var serveCmd = &cli.Command{
	Name: "serve",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name: "verbose",
		},
		&cli.BoolFlag{
			Name:    "log-json",
			Sources: cli.EnvVars("LOG_FORMAT_JSON"),
		},
		&cli.StringFlag{
			Name:     "api-listen",
			Value:    ":2510",
			Required: true,
			Sources:  cli.EnvVars("API_LISTEN"),
		},
		&cli.StringFlag{
			Name:    "metrics-listen",
			Value:   ":2511",
			Sources: cli.EnvVars("METRICS_LISTEN"),
		},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		ctx, cancel := context.WithCancel(ctx)
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		// chan for main server threads to return errors
		errchan := make(chan error, 10)

		logLevel := slog.LevelInfo
		if command.Bool("verbose") {
			logLevel = slog.LevelDebug
		}
		var handler slog.Handler
		if command.Bool("log-json") {
			handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
		} else {
			handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
		}
		log := slog.New(handler)
		slog.SetDefault(log)

		sc := serverContext{
			ctx:     ctx,
			cancel:  cancel,
			errchan: errchan,
			log:     log,
		}
		sc.wg.Add(1)
		go sc.ApiServerThread(command.String("api-listen"))
		metricsListen := command.String("metrics-listen")
		if metricsListen != "" {
			sc.wg.Add(1)
			go sc.MetricsThread(metricsListen)
		}

		// TODO: start other server tasks here

		// Wait for errors or finishes

		wgwaiter := make(chan struct{})
		go func() {
			sc.Wait()
			close(wgwaiter)
		}()

		signalcount := 0

		for {
			select {
			case err, ok := <-errchan:
				if !ok {
					log.Info("errchan closed, shutdown complete")
					return nil
				} else {
					log.Error("errchan", "err", err)
					xe := sc.Shutdown()
					if xe != nil {
						log.Error("shutdown", "err", xe)
					}
				}
			case <-wgwaiter:
				log.Info("all threads closed, exit")
				return nil
			case xc := <-signals:
				signalcount++
				if signalcount <= 1 {
					log.Info("received signal %#v", xc)
					go sc.Shutdown()
				} else {
					return fmt.Errorf("received signal %#v %d", xc, signalcount)
				}
			}
		}
	},
}

type serverContext struct {
	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup

	// err from a thread
	errchan chan<- error

	log *slog.Logger

	apiServer     *http.Server
	metricsServer *http.Server
}

func (sc *serverContext) ApiServerThread(addr string) {
	defer sc.wg.Done()
	defer sc.log.Info("ApiServerThread exit")

	var lc net.ListenConfig
	li, err := lc.Listen(sc.ctx, "tcp", addr)
	if err != nil {
		sc.errchan <- fmt.Errorf("ApiServerThread listen %w", err)
	}
	e := echo.New()
	e.HideBanner = true
	e.Use(MetricsMiddleware)
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowHeaders: []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization},
	}))
	e.GET("/_health", sc.healthz)
	// TODO: other handlers
	//e.POST("/v1/foo", sc.fooHandler)
	//e.GET("/v1/bar", sc.barHandler)
	sc.apiServer = &http.Server{
		Handler: e,
	}
	sc.errchan <- sc.apiServer.Serve(li)
}

func (sc *serverContext) healthz(c echo.Context) error {
	// TODO: check database or upstream health?
	return c.String(http.StatusOK, "ok")
}

// MetricsThread serves /metrics and /debug/pprof/*
func (sc *serverContext) MetricsThread(addr string) {
	defer sc.wg.Done()
	defer sc.log.Info("MetricsThread exit")

	http.DefaultServeMux.Handle("/metrics", promhttp.Handler())
	sc.metricsServer = &http.Server{
		Addr:    addr,
		Handler: http.DefaultServeMux,
	}
	sc.errchan <- sc.metricsServer.ListenAndServe()
}

// non-blocking start of shutdown, call serverContext.Wait()
func (sc *serverContext) Shutdown() error {
	sc.cancel()
	var sherrs []error
	err := sc.apiServer.Shutdown(context.Background())
	if err != nil {
		sherrs = append(sherrs, err)
	}
	if sc.metricsServer != nil {
		err = sc.metricsServer.Shutdown(context.Background())
		if err != nil {
			sherrs = append(sherrs, err)
		}
	}
	// TODO: shutdown other server tasks here?

	if len(sherrs) == 1 {
		return sherrs[0]
	}
	if len(sherrs) > 0 {
		return errors.Join(sherrs...)
	}
	return nil
}

func (sc *serverContext) Wait() {
	sc.wg.Wait()
	close(sc.errchan)
}

func errchanLogger(log *slog.Logger, errchan <-chan error) {
	for err := range errchan {
		log.Error("errchan", "err", err)
	}
}
