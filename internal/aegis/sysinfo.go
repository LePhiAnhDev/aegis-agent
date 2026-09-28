package aegis

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// hostPath is where the Docker installation mounts files of the host
// (/etc/hostname and /etc/os-release), read-only.
const hostPath = "/host"

// systemInfo is what the agent says about the server in every report.
type systemInfo struct {
	Hostname string
	OS       string
	Arch     string
	Runtime  string
}

func readSystemInfo(runtimeOverride string) systemInfo {
	return systemInfo{
		Hostname: hostname(),
		OS:       osName(),
		Arch:     runtime.GOARCH,
		Runtime:  detectRuntime(runtimeOverride),
	}
}

// hostname prefers the host's name over the container's random one.
func hostname() string {
	if data, err := os.ReadFile(hostPath + "/hostname"); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			return name
		}
	}
	name, _ := os.Hostname()
	return name
}

// osName is PRETTY_NAME from the host's os-release, e.g. "Ubuntu 24.04.1 LTS".
func osName() string {
	for _, path := range []string{hostPath + "/os-release", "/etc/os-release", "/usr/lib/os-release"} {
		if name := prettyName(path); name != "" {
			return name
		}
	}
	return runtime.GOOS
}

func prettyName(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(value, `"'`)
		}
	}
	return ""
}

func detectRuntime(override string) string {
	switch override {
	case "docker", "systemd", "other":
		return override
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	// systemd sets INVOCATION_ID for every service it starts.
	if os.Getenv("INVOCATION_ID") != "" {
		return "systemd"
	}
	return "other"
}
