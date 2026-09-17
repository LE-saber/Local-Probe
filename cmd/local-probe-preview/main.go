package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LE-saber/Local-Probe/internal/previewapp"
	"github.com/LE-saber/Local-Probe/internal/previewconnect"
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
	controller, err := previewconnect.New(previewconnect.DefaultOptions(defaultRepoRoot()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "preview_connector_invalid")
		os.Exit(1)
	}
	// Closing the window only hides it to the tray. Choosing Exit ends Run and
	// then stops only the two child processes owned by this Preview instance.
	defer controller.Stop()
	model := previewui.NewPreviewAppModel(app)
	connector := previewConnector{controller: controller}
	workspaceAdmin, err := newWorkspaceAdmin(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "preview_workspace_config_invalid")
		os.Exit(1)
	}
	workspaceManager := newPreviewWorkspaceManager(workspaceAdmin, controller)
	if err := previewui.Run(previewui.RunOptions{Model: model, Connector: connector, WorkspaceManager: workspaceManager, Title: "Local-Probe Preview"}); err != nil {
		// Keep startup diagnostics stable and free of paths, OS messages, and
		// any future adapter data. The GUI itself also uses stable categories.
		fmt.Fprintln(os.Stderr, stableError(err))
		os.Exit(1)
	}
}

func defaultRepoRoot() string {
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(filepath.Dir(exe))
	}
	root, err := filepath.Abs(".")
	if err == nil {
		return root
	}
	return "."
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
