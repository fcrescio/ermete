package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ermete/internal/config"
	"ermete/internal/httpapi"
	"ermete/internal/observability"
	"ermete/internal/session"
	"ermete/internal/storage"
	wrtc "ermete/internal/webrtc"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	logger, err := observability.NewLogger(cfg.LogLevel)
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	store, err := storage.NewFrameStore(cfg.DataDir)
	if err != nil {
		logger.Fatal("failed to init storage", zap.Error(err))
	}
	sessions := session.NewManager(cfg.SessionPolicy)
	metrics := observability.NewMetrics(prometheus.DefaultRegisterer)
	webrtcSvc, err := wrtc.NewService(cfg, logger, metrics, sessions, store)
	if err != nil {
		logger.Fatal("failed to init webrtc", zap.Error(err))
	}
	router := httpapi.NewRouter(cfg, logger, metrics, store, sessions, webrtcSvc)

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout}
	go func() {
		logger.Info("ermete listening", zap.String("addr", cfg.HTTPAddr))
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			err = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("server failed", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
	logger.Info("server stopped", zap.Duration("grace", cfg.ShutdownGracePeriod), zap.Time("at", time.Now().UTC()))
}
