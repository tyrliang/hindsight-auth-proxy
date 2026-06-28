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

	// ACLFile is the path to a YAML ACL file on disk. Used when S3 is not
	// configured (local dev, tests). Set ACL_FILE or use the ACL_S3_* vars below.
	ACLFile string `env:"ACL_FILE" envDefault:"/app/acl.yaml"`

	// ── ACL S3 source (Railway Storage Bucket / any S3-compatible store) ──
	// When ACL_S3_BUCKET is non-empty the proxy loads the ACL object from S3
	// at boot (and re-fetches on SIGHUP); ACL_FILE is then ignored.
	ACLS3Endpoint        string `env:"ACL_S3_ENDPOINT"`
	ACLS3Bucket          string `env:"ACL_S3_BUCKET"`
	ACLS3Key             string `env:"ACL_S3_KEY"`
	ACLS3Region          string `env:"ACL_S3_REGION" envDefault:"us-east-1"`
	ACLS3AccessKeyID     string `env:"ACL_S3_ACCESS_KEY_ID"`
	ACLS3SecretAccessKey string `env:"ACL_S3_SECRET_ACCESS_KEY"`
	ACLS3UsePathStyle    bool   `env:"ACL_S3_USE_PATH_STYLE" envDefault:"false"`

	// DevIdentityHeader enables dev mode: plain TCP listener instead of tsnet,
	// and reads the caller identity from this HTTP header instead of WhoIs.
	// When set, TS_HOSTNAME and TS_AUTHKEY are not required.
	// Example: X-Dev-User
	DevIdentityHeader string `env:"DEV_IDENTITY_HEADER"`
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

	// TS_HOSTNAME and TS_AUTHKEY are required unless running in dev mode.
	if Cfg.DevIdentityHeader == "" {
		if Cfg.TSHostname == "" {
			errs = append(errs, fmt.Errorf("TS_HOSTNAME is required when DEV_IDENTITY_HEADER is not set"))
		}
		if Cfg.TSAuthKey == "" {
			errs = append(errs, fmt.Errorf("TS_AUTHKEY is required when DEV_IDENTITY_HEADER is not set"))
		}
	}

	// Validate S3 config: if the bucket is set all required fields must be present.
	if Cfg.ACLS3Bucket != "" {
		var missing []string
		if Cfg.ACLS3Endpoint == "" {
			missing = append(missing, "ACL_S3_ENDPOINT")
		}
		if Cfg.ACLS3Key == "" {
			missing = append(missing, "ACL_S3_KEY")
		}
		if Cfg.ACLS3AccessKeyID == "" {
			missing = append(missing, "ACL_S3_ACCESS_KEY_ID")
		}
		if Cfg.ACLS3SecretAccessKey == "" {
			missing = append(missing, "ACL_S3_SECRET_ACCESS_KEY")
		}
		if len(missing) > 0 {
			errs = append(errs, fmt.Errorf("ACL_S3_BUCKET set but missing: %v", missing))
		}
	}

	if len(errs) > 0 {
		for _, e := range errs {
			logger.StderrWithSource.Error("configuration error", slog.Any("error", e))
		}
		os.Exit(1)
	}
}
