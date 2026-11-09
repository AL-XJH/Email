package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	smtpserver "github.com/emersion/go-smtp"
	"golang.org/x/net/netutil"

	"lanqin-email-api/internal/app"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := app.LoadConfig()

	svc, err := app.New(cfg, logger)
	if err != nil {
		logger.Error("failed to initialize app", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           svc.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	submissionServers := &app.SubmissionServers{}
	var submissionTLSConfig *tls.Config
	if strings.TrimSpace(cfg.SubmissionAddr) != "" || strings.TrimSpace(cfg.SubmissionTLSAddr) != "" {
		tlsConfig, err := app.LoadServerTLSConfig(cfg)
		if err != nil {
			logger.Error("failed to initialize TLS config", "error", err)
			os.Exit(1)
		}
		submissionTLSConfig = tlsConfig
		submissionServers = svc.NewSubmissionServers(tlsConfig)
	}

	go func() {
		logger.Info("LanQin API listening", "addr", cfg.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()
	// 提交端口是公网可连的：不限连接数时，每个连接都占一个 goroutine + 45s，
	// 洪泛能把 webmail/IMAP/worker 一起饿死。200 并发之后直接拒绝新连接。
	const submissionConnectionLimit = 200
	if submissionServers.Plain != nil {
		listener, err := net.Listen("tcp", cfg.SubmissionAddr)
		if err != nil {
			logger.Error("failed to listen for SMTP submission", "error", err)
			os.Exit(1)
		}
		listener = netutil.LimitListener(listener, submissionConnectionLimit)
		go func() {
			logger.Info("LanQin SMTP submission listening", "addr", cfg.SubmissionAddr)
			if err := submissionServers.Plain.Serve(listener); err != nil && !errors.Is(err, smtpserver.ErrServerClosed) {
				logger.Error("smtp submission server stopped unexpectedly", "error", err)
				os.Exit(1)
			}
		}()
	}
	if submissionServers.TLS != nil {
		listener, err := net.Listen("tcp", cfg.SubmissionTLSAddr)
		if err != nil {
			logger.Error("failed to listen for SMTP submission TLS", "error", err)
			os.Exit(1)
		}
		// LimitListener 的包装会隐藏 *tls.Conn 类型，go-smtp 就认为连接没加密、
		// 拒绝 AUTH（523 TLS is required）。必须先限流再套 TLS。
		listener = tls.NewListener(netutil.LimitListener(listener, submissionConnectionLimit), submissionTLSConfig)
		go func() {
			logger.Info("LanQin SMTP implicit TLS submission listening", "addr", cfg.SubmissionTLSAddr)
			if err := submissionServers.TLS.Serve(listener); err != nil && !errors.Is(err, smtpserver.ErrServerClosed) {
				logger.Error("smtp tls submission server stopped unexpectedly", "error", err)
				os.Exit(1)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown failed", "error", err)
		os.Exit(1)
	}
	if err := submissionServers.Shutdown(shutdownCtx); err != nil {
		logger.Error("smtp submission shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}
