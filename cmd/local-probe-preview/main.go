package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LE-saber/Local-Probe/internal/previewapp"
	"github.com/LE-saber/Local-Probe/internal/previewui"
)

func main() {
	defaultConfig := defaultConfigPath()
	defaultAudit := defaultAuditPath()
	configPath := flag.String("config", defaultConfig, "local-probe config path")
	auditDir := flag.String("audit-dir", defaultAudit, "local-probe audit directory")
	showVersion := flag.Bool("version", false, "show Preview version")
	flag.Parse()
	if *showVersion {
		fmt.Println(previewui.Version)
		return
	}

	app, err := previewapp.New(previewapp.Options{ConfigPath: *configPath, AuditDir: *auditDir})
	if err != nil {
		fmt.Fprintln(os.Stderr, stableError(err))
		os.Exit(1)
	}
	defer app.Close()
	model := previewui.NewPreviewAppModel(app)
	if err := previewui.Run(previewui.RunOptions{Model: model, Title: "Local-Probe Preview"}); err != nil {
		// Keep startup diagnostics stable and free of paths, OS messages, and
		// any future adapter data. The GUI itself also uses stable categories.
		fmt.Fprintln(os.Stderr, stableError(err))
		os.Exit(1)
	}
}

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(filepath.Dir(exe)), ".runtime", "local-probe.json")
		return candidate
	}
	return filepath.Join(".runtime", "local-probe.json")
}

func defaultAuditPath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return filepath.Join("Local-Probe", "audit")
	}
	return filepath.Join(base, "Local-Probe", "audit")
}

func stableError(err error) string {
	switch {
	case errors.Is(err, previewapp.ErrInvalidOptions):
		return "preview_invalid_options"
	case errors.Is(err, previewapp.ErrConfigInvalid):
		return "preview_config_invalid"
	case errors.Is(err, previewui.ErrAlreadyRunning):
		return "preview_already_running"
	case errors.Is(err, previewui.ErrUnsupportedPlatform):
		return "preview_unsupported_platform"
	case errors.Is(err, previewui.ErrUnavailable):
		return "preview_unavailable"
	default:
		return "preview_start_failed"
	}
}
