// local-probe-admin is a local, offline file-rule management command. It has
// no network listener, credential resolution, or execution/approval endpoint.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

const maxRequestBytes = 128 << 10

type ruleRequest struct {
	Update  workspaceadmin.RuleUpdate   `json:"update"`
	Samples []workspaceadmin.RuleSample `json:"samples,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "local-probe-admin:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("local-probe-admin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "absolute path to private local-probe.json; no automatic discovery")
	connectionID := flags.String("connection-id", "", "explicit configured connection")
	action := flags.String("action", "show", "show, preview, or apply file rules")
	revision := flags.String("revision", "", "revision from show, required for preview/apply")
	requestPath := flags.String("request", "", "absolute rule request JSON file; '-' reads bounded JSON from stdin")
	offline := flags.Bool("offline", false, "attest all runtimes using this config have been stopped; mandatory for apply")
	approve := flags.Bool("approve", false, "local user consent to this exact preview/revision; mandatory for apply")
	acknowledge := flags.String("ack-connections", "", "comma-separated exact affected connections from preview")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*configPath) || *connectionID == "" {
		return errors.New("provide absolute -config and explicit -connection-id; no positional arguments")
	}
	if *action != "show" && *action != "preview" && *action != "apply" {
		return errors.New("-action must be show, preview, or apply")
	}
	if *action == "apply" && (!*offline || !*approve) {
		return errors.New("apply requires -offline and -approve after local review; live revocation is not implemented")
	}
	if *action == "show" && (*requestPath != "" || *revision != "" || *approve || *offline || *acknowledge != "") {
		return errors.New("show does not accept mutation arguments")
	}
	if *action == "preview" && (*approve || *offline || *acknowledge != "") {
		return errors.New("preview does not accept apply arguments")
	}
	if *action != "show" && (*revision == "" || *requestPath == "") {
		return errors.New("preview/apply require -revision and -request")
	}
	var request ruleRequest
	if *action != "show" {
		var err error
		request, err = loadRequest(*requestPath, stdin)
		if err != nil {
			return err
		}
	}
	store, err := config.NewFileStore(*configPath)
	if err != nil {
		return errors.New("invalid configuration location")
	}
	manager, err := workspaceadmin.New(store)
	if err != nil {
		return errors.New("cannot initialize local configuration manager")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var result any
	switch *action {
	case "show":
		result, err = manager.FileRules(ctx, *connectionID)
	case "preview":
		result, err = manager.PreviewRules(ctx, *connectionID, *revision, request.Update, request.Samples)
	case "apply":
		// Revalidate sample paths against this exact revision before persistence.
		// SetRules subsequently re-reads under the OS mutex and repeats CAS.
		if _, err = manager.PreviewRules(ctx, *connectionID, *revision, request.Update, request.Samples); err == nil {
			var acknowledged []string
			if *acknowledge != "" {
				acknowledged = strings.Split(*acknowledge, ",")
			}
			result, err = manager.SetRules(ctx, *connectionID, *revision, request.Update, acknowledged)
		}
	}
	if err != nil {
		if code := workspaceadmin.ProblemCode(err); code != "" {
			return fmt.Errorf("%s: %s", code, workspaceadmin.Remediation(err))
		}
		if errors.Is(err, context.Canceled) {
			return errors.New("operation cancelled")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("operation timed out")
		}
		return errors.New("local rule operation failed")
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return errors.New("cannot write rule result")
	}
	return nil
}

func loadRequest(location string, stdin io.Reader) (ruleRequest, error) {
	reader := stdin
	if location != "-" {
		if !filepath.IsAbs(location) {
			return ruleRequest{}, errors.New("request path must be absolute or '-' for stdin")
		}
		file, err := os.Open(location)
		if err != nil {
			return ruleRequest{}, errors.New("cannot open rule request")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxRequestBytes {
			return ruleRequest{}, errors.New("rule request must be a bounded ordinary JSON file")
		}
		reader = file
	}
	if reader == nil {
		return ruleRequest{}, errors.New("rule request input required")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	if err != nil || len(data) > maxRequestBytes {
		return ruleRequest{}, errors.New("rule request exceeds 128 KiB or cannot be read")
	}
	// Exact lower-case keys only. Reject duplicates before struct decoding so
	// last-wins/case-fold behavior cannot hide a different reviewed payload.
	keyDecoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkRequestJSON(keyDecoder, 0); err != nil {
		return ruleRequest{}, errors.New("invalid, unknown, or duplicate rule request fields")
	}
	if _, err := keyDecoder.Token(); err != io.EOF {
		return ruleRequest{}, errors.New("exactly one rule request object required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request *ruleRequest
	if err := decoder.Decode(&request); err != nil || request == nil {
		return ruleRequest{}, errors.New("invalid rule request schema")
	}
	return *request, nil
}

func walkRequestJSON(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("request nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] || !requestKey(key) {
				return errors.New("invalid key")
			}
			seen[key] = true
			if err := walkRequestJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid object")
		}
	case '[':
		for decoder.More() {
			if err := walkRequestJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid array")
		}
	default:
		return errors.New("unexpected delimiter")
	}
	return nil
}

func requestKey(key string) bool {
	switch key {
	case "update", "samples", "root_id", "deny_patterns", "ignore_patterns", "path", "directory":
		return true
	default:
		return false
	}
}
