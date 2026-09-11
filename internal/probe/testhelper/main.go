// Command testhelper is used only by internal/probe's deterministic tests.
// Its executable is copied/renamed per test so the production probe still
// receives a fixed --version action and no caller-controlled arguments.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	mode := strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0]))
	child := len(os.Args) > 1 && os.Args[1] == "child"
	switch mode {
	case "fake-node":
		fmt.Fprintln(os.Stdout, "v1.2.3")
	case "fake-git":
		fmt.Fprintln(os.Stdout, "git version 9.8.7")
	case "fake-python":
		fmt.Fprintln(os.Stderr, "Python 3.12.4")
	case "fake-env":
		if os.Getenv("LOCAL_PROBE_SECRET") != "" || strings.Contains(os.Getenv("PATH"), "LOCAL_PROBE_MALICIOUS") {
			fmt.Fprintln(os.Stdout, "v0.0.0")
			return
		}
		fmt.Fprintln(os.Stdout, "v1.2.3")
	case "fake-overoutput":
		fmt.Fprintln(os.Stdout, "v1.2.3")
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", 70<<10))
	case "fake-stderroveroutput":
		fmt.Fprintln(os.Stdout, "v1.2.3")
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("x", 20<<10))
	case "fake-path-output":
		workingDirectory, _ := os.Getwd()
		fmt.Fprintln(os.Stdout, workingDirectory)
	case "fake-timeout":
		if !child {
			childCommand := exec.Command(os.Args[0], "child")
			childCommand.Stdout = os.Stdout
			childCommand.Stderr = os.Stderr
			if err := childCommand.Start(); err != nil {
				fmt.Fprintln(os.Stderr, "child-start-failed")
			}
		}
		if child {
			fmt.Fprintln(os.Stdout, os.Getpid())
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown helper")
		os.Exit(2)
	}
}
