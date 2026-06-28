package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"main/internal/aclsource"
	"main/internal/authz"
	"main/internal/config"
	"main/internal/logger"
	"main/internal/proxy"

	"tailscale.com/tsnet"
)

func main() {
	// ── Resolve ACL source ───────────────────────────────────────────────────
	// S3 mode (ACL_S3_BUCKET set): fetch YAML from the Railway Storage Bucket.
	// File mode (ACL_FILE, default): read a YAML file from disk — local dev/tests.
	src := aclsource.New(config.Cfg.ACLFile, aclsource.S3{
		Endpoint:        config.Cfg.ACLS3Endpoint,
		Bucket:          config.Cfg.ACLS3Bucket,
		Key:             config.Cfg.ACLS3Key,
		Region:          config.Cfg.ACLS3Region,
		AccessKeyID:     config.Cfg.ACLS3AccessKeyID,
		SecretAccessKey: config.Cfg.ACLS3SecretAccessKey,
		UsePathStyle:    config.Cfg.ACLS3UsePathStyle,
	})

	fetchCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	aclData, aclSrc, err := src.Fetch(fetchCtx)
	cancel()
	if err != nil {
		logger.Stderr.Error("failed to fetch ACL", slog.Any("error", err))
		os.Exit(1)
	}
	acl, err := authz.LoadBytes(aclData)
	if err != nil {
		logger.Stderr.Error("failed to load ACL", slog.String("source", aclSrc), slog.Any("error", err))
		os.Exit(1)
	}
	logger.Stdout.Info("ACL loaded", slog.String("source", aclSrc))

	var (
		ln    net.Listener
		whoIs proxy.WhoIsFunc
		// dial is intentionally nil: upstream traffic goes via the host network
		// (Railway private networking, localhost in dev mode), not through the
		// tailnet. The tailnet is used only for the client-facing listener and
		// WhoIs identity — not for the proxy → Hindsight leg.
	)

	if config.Cfg.DevIdentityHeader != "" {
		// ── Dev mode ─────────────────────────────────────────────────────────
		// Plain TCP listener; identity comes from a request header.
		// No tsnet, no tailscale deps at runtime.
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", config.Cfg.ListenPort))
		if err != nil {
			logger.Stderr.Error("listen failed", slog.Any("error", err))
			os.Exit(1)
		}
		logger.Stdout.Info("🔧 Dev mode: plain TCP listener",
			slog.Int("port", config.Cfg.ListenPort),
			slog.String("identity-header", config.Cfg.DevIdentityHeader),
		)
		// whoIs and dial stay nil; the proxy handler reads the dev header.
	} else {
		// ── Production mode ───────────────────────────────────────────────────
		// Join the tailnet as a stable tsnet node; upstream is dialed over tsnet.
		ts := &tsnet.Server{
			Hostname:     config.Cfg.TSHostname,
			AuthKey:      config.Cfg.TSAuthKey,
			Dir:          config.Cfg.TSStateDir,
			RunWebClient: false,
			Ephemeral:    config.Cfg.TSEphemeral,
			UserLogf: func(format string, v ...any) {
				logger.Stdout.Info(fmt.Sprintf(format, v...))
			},
		}

		if _, err := ts.Up(context.Background()); err != nil {
			logger.Stderr.Error("tsnet up failed", slog.Any("error", err))
			os.Exit(1)
		}
		defer ts.Close()

		ln, err = ts.Listen("tcp", fmt.Sprintf(":%d", config.Cfg.ListenPort))
		if err != nil {
			logger.Stderr.Error("tsnet listen failed", slog.Any("error", err))
			os.Exit(1)
		}

		lc, err := ts.LocalClient()
		if err != nil {
			logger.Stderr.Error("tsnet LocalClient failed", slog.Any("error", err))
			os.Exit(1)
		}

		// Wrap LocalClient.WhoIs to return only the email string.
		whoIs = func(ctx context.Context, addr string) (string, error) {
			resp, err := lc.WhoIs(ctx, addr)
			if err != nil {
				return "", err
			}
			if resp == nil || resp.UserProfile == nil {
				return "", nil
			}
			return resp.UserProfile.LoginName, nil
		}

		// Upstream uses the host network (Railway private networking).
		// Do NOT use ts.Dial here: machine-to-machine tailnet connections
		// between two Railway services are blocked by ACL and unnecessary —
		// HINDSIGHT_UPSTREAM_URL should point to the service's Railway private
		// domain (e.g. http://hindsight-app.railway.internal:8888).

		logger.Stdout.Info("🚀 Starting hindsight-auth-proxy",
			slog.String("ts-hostname", config.Cfg.TSHostname),
			slog.String("ts-state-dir", config.Cfg.TSStateDir),
			slog.Bool("ts-ephemeral", config.Cfg.TSEphemeral),
			slog.Int("listen-port", config.Cfg.ListenPort),
			slog.String("upstream", config.Cfg.HindsightUpstreamURL),
		)
	}

	h := proxy.New(proxy.Options{
		UpstreamURL:    config.Cfg.HindsightUpstreamURL,
		UpstreamToken:  config.Cfg.UpstreamToken,
		DevIdentityHdr: config.Cfg.DevIdentityHeader,
	}, acl, whoIs, nil)

	// ── SIGHUP: re-fetch ACL from source (file or S3) ────────────────────────
	// On SIGHUP the proxy re-fetches from the same source used at boot.
	// Fetch or parse failure keeps the previous ACL — the proxy stays live.
	// Note: on Railway (distroless image, no shell) the effective reload path is
	// a service restart — SIGHUP is most useful for local / non-distroless runs.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	go func() {
		for range sigs {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			data, src1, err := src.Fetch(ctx)
			cancel()
			if err != nil {
				logger.Stderr.Error("ACL reload failed (fetch); keeping previous ACL", slog.Any("error", err))
				continue
			}
			newACL, err := authz.LoadBytes(data)
			if err != nil {
				logger.Stderr.Error("ACL reload failed (parse); keeping previous ACL",
					slog.String("source", src1),
					slog.Any("error", err),
				)
				continue
			}
			h.SetACL(newACL)
			logger.Stdout.Info("ACL reloaded", slog.String("source", src1))
		}
	}()

	logger.Stdout.Info("listening", slog.Int("port", config.Cfg.ListenPort))

	if err := http.Serve(ln, h); err != nil {
		logger.Stderr.Error("http.Serve exited", slog.Any("error", err))
		os.Exit(1)
	}
}
