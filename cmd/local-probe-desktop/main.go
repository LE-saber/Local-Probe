package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/desktopbridge"
	"github.com/LE-saber/Local-Probe/internal/desktophost"
)

const desktopVersion = desktopbridge.GUIVersion

func main() {
	configFlag := flag.String("config", "", "local-probe config path")
	auditFlag := flag.String("audit-dir", "", "local-probe audit directory")
	transportFlag := flag.String("transport", string(config.TransportCloudflareNamed), "connection transport: cloudflare_named or openai_runtime")
	versionFlag := flag.Bool("version", false, "show desktop version")
	flag.Parse()
	if *versionFlag {
		fmt.Println(desktopVersion)
		return
	}
	root, err := executableRepoRoot()
	if err != nil {
		desktophost.ShowStartupError(desktophost.ErrSecureHost)
		os.Exit(1)
	}
	configPath := *configFlag
	if configPath == "" {
		configPath = filepath.Join(root, ".runtime", "local-probe.json")
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		desktophost.ShowStartupError(desktophost.ErrSecureHost)
		os.Exit(1)
	}
	auditDir := *auditFlag
	if auditDir == "" {
		base, baseErr := os.UserConfigDir()
		if baseErr != nil || base == "" {
			base = filepath.Join(root, ".runtime")
		}
		auditDir = filepath.Join(base, "Local-Probe", "audit")
	}
	auditDir, err = filepath.Abs(auditDir)
	if err != nil {
		desktophost.ShowStartupError(desktophost.ErrSecureHost)
		os.Exit(1)
	}
	err = desktophost.Run(desktophost.Options{
		ConfigPath: configPath,
		AuditDir:   auditDir,
		RepoRoot:   root,
		Transport:  *transportFlag,
	})
	if err == nil {
		return
	}
	desktophost.ShowStartupError(err)
	fmt.Fprintln(os.Stderr, stableError(err))
	os.Exit(1)
}

func executableRepoRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	root := filepath.Dir(filepath.Dir(exe))
	if root == "" || root == "." {
		return "", desktophost.ErrSecureHost
	}
	return filepath.Abs(root)
}

func stableError(err error) string {
	switch {
	case errors.Is(err, desktophost.ErrAlreadyRunning):
		return "desktop_already_running"
	case errors.Is(err, desktopbridge.ErrCloseStopFailed):
		return "owned_connection_stop_failed"
	case errors.Is(err, desktophost.ErrUnsupported):
		return "desktop_unsupported_platform"
	case errors.Is(err, desktophost.ErrSecureHost):
		return "desktop_security_initialization_failed"
	default:
		return "desktop_start_failed"
	}
}
