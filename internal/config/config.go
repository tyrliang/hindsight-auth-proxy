package config

import (
	"fmt"
	"log/slog"
	"os"

	"main/internal/logger"

	"github.com/caarlos0/env/v11"
)

// Config holds all runtime configuration for hindsight-auth-proxy.
type Config struct {
	// TSHostname is the MagicDNS hostname to register on the tailnet.
	// Required unless DEV_IDENTITY_HEADER is set.
	TSHostname string `env:"TS_HOSTNAME"`
	// TSAuthKey is the tailscale auth key.
	// Required unless DEV_IDENTITY_HEADER is set.
	TSAuthKey string `env:"TS_AUTHKEY"`
	// TSStateDir persists the tailscale node state. Mount a volume here in production
	// so the node retains a stable identity across restarts.
	TSStateDir string `env:"TS_STATE_DIR"`
	// TSEphemeral controls whether the tailnet node is ephemeral (auto-removed when offline).
	// Set false (default) so the MagicDNS name is stable.
	TSEphemeral bool `env:"TS_EPHEMERAL" envDefault:"false"`

	// ListenPort is the TCP port the proxy listens on.
	ListenPort int `env:"LISTEN_PORT" envDefault:"8888"`

	// HindsightUpstreamURL is the base URL of the upstream Hindsight instance,
	// reached via the tailnet (e.g. http://hindsight-dev.baiji-cloud.ts.net:8888).
	HindsightUpstreamURL string `env:"HINDSIGHT_UPSTREAM_URL,required"`

	// UpstreamToken is the bearer secret injected into every upstream request.
	// Must match HINDSIGHT_API_TENANT_API_KEY / HINDSIGHT_API_MCP_AUTH_TOKEN on Hindsight.
	UpstreamToken string `env:"HINDSIGHT_UPSTREAM_TOKEN,required"`

	// ACLFile is the path to a YAML ACL file on disk.
	// Used when ACL_YAML_CONTENT is not set.
	// Defaults to /app/acl.yaml but that path is NOT baked into the image —
	// use ACL_YAML_CONTENT (preferred for Railway) or mount a volume.
	ACLFile string `env:"ACL_FILE" envDefault:"/app/acl.yaml"`

	// ACLYamlContent is the raw YAML ACL content as an environment variable.
	// When set it takes priority over ACL_FILE: the proxy writes the content
	// to a temp file at startup and reloads from it on SIGHUP.
	// Recommended for Railway deployments — update the variable and redeploy.
	ACLYamlContent string `env:"ACL_YAML_CONTENT"`

	// DevIdentityHeader enables dev mode: plain TCP listener instead of tsnet,
	// and reads the caller identity from this HTTP header instead of WhoIs.
	// When set, TS_HOSTNAME and TS_AUTHKEY are not required.
	// Example: X-Dev-User
	DevIdentityHeader string `env:"DEV_IDENTITY_HEADER"`

	// ConnectProxyPort, when > 0, starts an HTTP CONNECT proxy on all interfaces
	// (Railway private network) that tunnels HTTPS connections via ts.Dial.
	// Set EGRESS_PROXY_PORT=4000 to enable. Clients then set:
	//   HTTPS_PROXY=http://<this-service.railway.internal>:<port>
	//   HINDSIGHT_API_LLM_BASE_URL=https://ai-proxy.baiji-cloud.ts.net/v1
	// The CONNECT tunnel carries the original TLS so hostname verification passes.
	// Only active in production (tsnet) mode — silently ignored in dev mode.
	ConnectProxyPort int `env:"EGRESS_PROXY_PORT" envDefault:"0"`

	// EgressWarmupTarget, when set, fires a background ts.Dial to this address
	// immediately after tsnet is up. This pre-establishes the Tailscale peer
	// route so the first real CONNECT request completes within httpx's 5s connect
	// timeout. Format: "host:port", e.g. "ai-proxy.baiji-cloud.ts.net:443".
	// Only active in production (tsnet) mode.
	EgressWarmupTarget string `env:"EGRESS_WARMUP_TARGET"`

	// TailscaleServeMode=true: bind to 127.0.0.1 only (loopback),
	// expecting tailscale serve to be the exclusive inbound gateway.
	// Guards against Tailscale-User-Login spoofing via Railway private net.
	TailscaleServeMode bool `env:"TAILSCALE_SERVE_MODE" envDefault:"false"`

	// InternalPort: loopback port for the proxy binary when TailscaleServeMode=true.
	// tailscale serve is configured to forward tailnet :ListenPort -> 127.0.0.1:InternalPort.
	InternalPort int `env:"INTERNAL_PORT" envDefault:"8889"`
}

// Cfg is the global parsed configuration, populated by init().
var Cfg = Config{}

func init() {
	var errs []error

	if err := env.Parse(&Cfg); err != nil {
		if e, ok := err.(env.AggregateError); ok {
			errs = append(errs, e.Errors...)
		} else {
			errs = append(errs, err)
		}
	}

	// TS_HOSTNAME and TS_AUTHKEY are required unless running in dev mode or serve mode.
	if Cfg.DevIdentityHeader == "" && !Cfg.TailscaleServeMode {
		if Cfg.TSHostname == "" {
			errs = append(errs, fmt.Errorf("TS_HOSTNAME is required when DEV_IDENTITY_HEADER is not set and TAILSCALE_SERVE_MODE is false"))
		}
		if Cfg.TSAuthKey == "" {
			errs = append(errs, fmt.Errorf("TS_AUTHKEY is required when DEV_IDENTITY_HEADER is not set and TAILSCALE_SERVE_MODE is false"))
		}
	}


	if len(errs) > 0 {
		for _, e := range errs {
			logger.StderrWithSource.Error("configuration error", slog.Any("error", e))
		}
		os.Exit(1)
	}
}
