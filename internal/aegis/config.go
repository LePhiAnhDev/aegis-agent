// Package aegis connects Aegis Agent to Aegis Cloud: it reports every backup,
// prune, check and forget of the repositories stored on Aegis Cloud storages,
// plus a heartbeat and an inventory of repositories and plans. Reporting is
// one-way: Aegis Cloud never sends instructions back, and nothing secret
// (repository passwords, environment, flags, file contents) ever leaves the
// server.
package aegis

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	envReportURL         = "AEGIS_REPORT_URL"
	envReportToken       = "AEGIS_REPORT_TOKEN"
	envInstanceID        = "AEGIS_INSTANCE_ID"
	envRepositories      = "AEGIS_REPOSITORIES"
	envHeartbeatInterval = "AEGIS_HEARTBEAT_INTERVAL"
	envRuntime           = "AEGIS_RUNTIME"

	defaultHeartbeatInterval = 5 * time.Minute
	inventoryInterval        = 30 * time.Minute
)

// Config is how this agent reaches Aegis Cloud, from the environment the
// installer wrote (docker-compose or /etc/aegis-agent/agent.env).
type Config struct {
	// ReportURL and ReportToken enable reporting; without them the agent runs
	// as a plain backup tool.
	ReportURL   string
	ReportToken string
	// InstanceID is the instance ID Aegis Cloud suggested for this server.
	InstanceID string
	// Repositories are the Aegis Cloud storages this server may write to.
	Repositories []RepositoryPreset
	// HeartbeatInterval is how often the agent reports even when idle.
	HeartbeatInterval time.Duration
	// AgentVersion and ResticVersion are reported in every heartbeat.
	AgentVersion  string
	ResticVersion string
	// Runtime overrides runtime detection: docker, systemd or other.
	Runtime string
}

// RepositoryPreset is one storage the server was attached to when it was
// installed: what the "Aegis Cloud storage" option of the repository form
// offers.
type RepositoryPreset struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	URI    string `json:"uri"`
}

// Enabled reports whether this agent reports to Aegis Cloud.
func (c Config) Enabled() bool {
	return c.ReportURL != "" && c.ReportToken != ""
}

// ConfigFromEnv reads the Aegis settings from the environment.
func ConfigFromEnv(agentVersion, resticVersion string) (Config, error) {
	cfg := Config{
		ReportURL:         strings.TrimSpace(os.Getenv(envReportURL)),
		ReportToken:       strings.TrimSpace(os.Getenv(envReportToken)),
		InstanceID:        strings.TrimSpace(os.Getenv(envInstanceID)),
		HeartbeatInterval: defaultHeartbeatInterval,
		AgentVersion:      agentVersion,
		ResticVersion:     resticVersion,
		Runtime:           strings.TrimSpace(os.Getenv(envRuntime)),
	}
	if (cfg.ReportURL == "") != (cfg.ReportToken == "") {
		return cfg, fmt.Errorf("%s and %s must be set together", envReportURL, envReportToken)
	}
	if cfg.ReportURL != "" {
		u, err := url.Parse(cfg.ReportURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return cfg, fmt.Errorf("%s must be an http(s) URL", envReportURL)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envHeartbeatInterval)); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval < time.Minute {
			return cfg, fmt.Errorf("%s must be a duration of at least 1m, e.g. 5m", envHeartbeatInterval)
		}
		cfg.HeartbeatInterval = interval
	}
	presets, err := ParseRepositoryPresets(os.Getenv(envRepositories))
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", envRepositories, err)
	}
	cfg.Repositories = presets
	return cfg, nil
}

// ParseRepositoryPresets reads AEGIS_REPOSITORIES: entries separated by ";",
// each "<percent-encoded name>|<region>|<repository URI>". The format needs no
// quoting in a docker-compose file or a systemd environment file.
func ParseRepositoryPresets(raw string) ([]RepositoryPreset, error) {
	var presets []RepositoryPreset
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		fields := strings.SplitN(entry, "|", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("entry %q is not name|region|uri", entry)
		}
		name, err := url.PathUnescape(fields[0])
		if err != nil {
			return nil, fmt.Errorf("entry %q: name: %w", entry, err)
		}
		repo, ok := ParseR2RepoURI(fields[2])
		if !ok {
			return nil, fmt.Errorf("entry %q: not an Aegis Cloud storage URI", entry)
		}
		presets = append(presets, RepositoryPreset{Name: name, Region: fields[1], URI: repo.URI})
	}
	return presets, nil
}

// aegisBucket matches the bucket names Aegis Cloud creates.
var aegisBucket = regexp.MustCompile(`^aegis-[0-9a-z]{6}-(apac|weur|eeur|wnam|enam|oc)-[0-9a-z]{6}$`)

// r2Host matches Cloudflare R2's S3 endpoint for an account.
var r2Host = regexp.MustCompile(`^[0-9a-f]{32}\.r2\.cloudflarestorage\.com$`)

// R2Repo is a restic repository on an Aegis Cloud storage.
type R2Repo struct {
	URI    string
	Bucket string
	// Prefix is the folder inside the bucket, without leading or trailing slashes.
	Prefix string
}

// ParseR2RepoURI recognizes s3:https://<account>.r2.cloudflarestorage.com/<aegis bucket>/<folder>.
// Only these repositories are ever reported; any other repository stays private.
func ParseR2RepoURI(uri string) (R2Repo, bool) {
	uri = strings.TrimSpace(uri)
	rest, ok := strings.CutPrefix(uri, "s3:")
	if !ok {
		return R2Repo{}, false
	}
	u, err := url.Parse(rest)
	if err != nil || u.Scheme != "https" || !r2Host.MatchString(u.Host) {
		return R2Repo{}, false
	}
	path := strings.Trim(u.Path, "/")
	bucket, prefix, _ := strings.Cut(path, "/")
	if !aegisBucket.MatchString(bucket) {
		return R2Repo{}, false
	}
	return R2Repo{URI: uri, Bucket: bucket, Prefix: strings.Trim(prefix, "/")}, true
}
