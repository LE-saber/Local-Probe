//go:build windows

package workspaceadmin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
)

// Two real test executables cooperate only through the Windows named mutex
// and FileStore. No shared Go lock or fake persistence mediates the CAS race.
func TestRuleCASAcrossWindowsProcesses(t *testing.T) {
	env := newTestWorkspace(t, false)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type child struct {
		command *exec.Cmd
		input   io.WriteCloser
		result  chan string
	}
	children := []child{}
	for i := 0; i < 2; i++ {
		command := exec.CommandContext(ctx, executable, "-test.run=^TestRuleWriterProcessHelper$", "--", "rule-writer-helper", env.configPath, env.initial.Revision())
		input, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		result := make(chan string, 1)
		scanner := bufio.NewScanner(output)
		ready := make(chan bool, 1)
		go func() {
			if !scanner.Scan() || scanner.Text() != "ready" {
				ready <- false
				result <- "missing_ready"
				return
			}
			ready <- true
			if !scanner.Scan() {
				result <- "missing_result"
				return
			}
			result <- scanner.Text()
		}()
		select {
		case ok := <-ready:
			if !ok {
				t.Fatal("rule writer did not initialize")
			}
		case <-ctx.Done():
			t.Fatal("rule writer initialization timed out")
		}
		children = append(children, child{command: command, input: input, result: result})
		t.Cleanup(func() {
			_ = input.Close()
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			_ = command.Wait()
		})
	}
	for _, child := range children {
		if _, err := io.WriteString(child.input, "go\n"); err != nil {
			t.Fatal(err)
		}
		_ = child.input.Close()
	}
	winners, conflicts := 0, 0
	for _, child := range children {
		select {
		case result := <-child.result:
			switch result {
			case "ok":
				winners++
			case string(CodeRevisionConflict):
				conflicts++
			default:
				t.Fatalf("unexpected writer result=%s", result)
			}
		case <-ctx.Done():
			t.Fatal("rule writer result timed out")
		}
		if err := child.command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
	saved, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := saved.Config().Profile("profile-a")
	if len(profile.DenyPatterns()) != 1 || profile.DenyPatterns()[0] != "*.key" {
		t.Fatal("winning rule transaction was not persisted")
	}
}

func TestRuleWriterProcessHelper(t *testing.T) {
	arguments := os.Args
	if len(arguments) < 4 || arguments[len(arguments)-3] != "rule-writer-helper" {
		return
	}
	store, err := config.NewFileStore(arguments[len(arguments)-2])
	if err != nil {
		os.Exit(2)
	}
	manager, err := New(store)
	if err != nil {
		os.Exit(2)
	}
	fmt.Println("ready")
	if !bufio.NewScanner(os.Stdin).Scan() {
		os.Exit(3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = manager.SetRules(ctx, "chatgpt-local", arguments[len(arguments)-1], RuleUpdate{Deny: patterns("*.key")}, []string{"chatgpt-local"})
	if err == nil {
		fmt.Println("ok")
	} else {
		code := ProblemCode(err)
		if strings.TrimSpace(string(code)) == "" {
			os.Exit(4)
		}
		fmt.Println(code)
	}
	os.Exit(0)
}
