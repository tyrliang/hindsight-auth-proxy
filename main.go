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

	"main/internal/authz"
	"main/internal/config"
	"main/internal/logger"
	"main/internal/proxy"

	"tailscale.com/tsnet"
)

func main() {
	// ── Resolve ACL source ───────────────────────────────────────────────────
	// ACL_YAML_CONTENT (env var) takes priority over ACL_FILE (disk path).
	// This lets Railway operators update the ACL by editing a single variable
	// and redeploying — no image rebuild, no volume mount needed.
	aclPath := config.Cfg.ACLFile
	if config.Cfg.ACLYamlContent != "" {
		f, err := os.CreateTemp("", "hindsight-acl-*.yaml")
		if err != nil {
			logger.Stderr.Error("failed to create temp ACL file", slog.Any("error", err))
			os.Exit(1)
		}
		if _, err := f.WriteString(config.Cfg.ACLYamlContent); err != nil {
			logger.Stderr.Error("failed to write ACL_YAML_CONTENT to temp file", slog.Any("error", err))
			os.Exit(1)
		}
		f.Close()
		aclPath = f.Name()
		logger.Stdout.Info("ACL loaded from ACL_YAML_CONTENT env var",
			slog.String("temp-file", aclPath),
		)
	}

	acl, err := authz.Load(aclPath)
	if err != nil {
		logger.Stderr.Error("failed to load ACL",
			slog.String("file", aclPath),
			slog.Any("error", err),
		)
		os.Exit(1)
	}

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

	// ── SIGHUP: hot-reload the ACL without downtime ───────────────────────────
	// When ACL_YAML_CONTENT is set, the content is fixed for this process's
	// lifetime — SIGHUP re-parses the same temp file (useful after manual edits
	// to the temp file, e.g. via kubectl exec or railway shell). For Railway
	// env-var updates, redeploy to pick up the new content.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	go func() {
		for range sigs {
			newACL, err := authz.Load(aclPath)
			if err != nil {
				logger.Stderr.Error("ACL reload failed; keeping previous ACL",
					slog.String("file", aclPath),
					slog.Any("error", err),
				)
				continue
			}
			h.SetACL(newACL)
			logger.Stdout.Info("ACL reloaded", slog.String("file", aclPath))
		}
	}()

	logger.Stdout.Info("listening", slog.Int("port", config.Cfg.ListenPort))

	if err := http.Serve(ln, h); err != nil {
		logger.Stderr.Error("http.Serve exited", slog.Any("error", err))
		os.Exit(1)
	}
}
