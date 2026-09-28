package aegis

import "testing"

const testURI = "s3:https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/aegis-abc123-apac-xyz789/servers/agt_exampleid001"

func TestParseR2RepoURI(t *testing.T) {
	repo, ok := ParseR2RepoURI(testURI)
	if !ok || repo.Bucket != "aegis-abc123-apac-xyz789" || repo.Prefix != "servers/agt_exampleid001" {
		t.Fatalf("ParseR2RepoURI = %+v, %v", repo, ok)
	}
	if repo, ok := ParseR2RepoURI(testURI + "/"); !ok || repo.Prefix != "servers/agt_exampleid001" {
		t.Errorf("a trailing slash is ignored, got %+v", repo)
	}
	for _, uri := range []string{
		"/srv/backups",
		"sftp:backup@example.com:/repo",
		"s3:https://s3.amazonaws.com/aegis-abc123-apac-xyz789/x",
		"s3:https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/other-project-backups/x",
		"s3:http://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/aegis-abc123-apac-xyz789/x",
		"rclone:r2:aegis-abc123-apac-xyz789",
	} {
		if _, ok := ParseR2RepoURI(uri); ok {
			t.Errorf("%q is not an Aegis Cloud storage", uri)
		}
	}
}

func TestParseRepositoryPresets(t *testing.T) {
	presets, err := ParseRepositoryPresets("Asia%20primary|apac|" + testURI + " ; Europe%2C%20replica|weur|" +
		"s3:https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/aegis-abc123-weur-uvw456/servers/agt_exampleid001;")
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) != 2 || presets[0].Name != "Asia primary" || presets[0].Region != "apac" ||
		presets[1].Name != "Europe, replica" || presets[1].URI == "" {
		t.Fatalf("presets = %+v", presets)
	}
	if presets, err := ParseRepositoryPresets(""); err != nil || presets != nil {
		t.Errorf("no presets: %v, %v", presets, err)
	}
	for _, raw := range []string{"just-a-name", "Name|apac|/local/path", "Bad%zz|apac|" + testURI} {
		if _, err := ParseRepositoryPresets(raw); err == nil {
			t.Errorf("%q must be refused", raw)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(envReportURL, "https://aegis-api.ailabx.vn/v1/agent/reports")
	t.Setenv(envReportToken, "aegis_rt_x")
	t.Setenv(envInstanceID, "web-prod-01")
	t.Setenv(envRepositories, "Asia|apac|"+testURI)
	t.Setenv(envHeartbeatInterval, "")
	cfg, err := ConfigFromEnv("1.0.0", "0.19.1")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled() || cfg.InstanceID != "web-prod-01" || len(cfg.Repositories) != 1 ||
		cfg.HeartbeatInterval != defaultHeartbeatInterval {
		t.Fatalf("config = %+v", cfg)
	}

	t.Setenv(envReportToken, "")
	if _, err := ConfigFromEnv("1.0.0", "0.19.1"); err == nil {
		t.Error("a report URL without a token must be refused")
	}
	t.Setenv(envReportURL, "")
	if cfg, err := ConfigFromEnv("1.0.0", "0.19.1"); err != nil || cfg.Enabled() {
		t.Errorf("without both, reporting is off: %+v, %v", cfg, err)
	}
	t.Setenv(envHeartbeatInterval, "10s")
	if _, err := ConfigFromEnv("1.0.0", "0.19.1"); err == nil {
		t.Error("a heartbeat under a minute must be refused")
	}
}
