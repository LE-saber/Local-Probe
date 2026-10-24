// local-probe-operations is an owner-only approval/inspection CLI. It never
// runs commands and has no network listener or remote approval endpoint.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/LE-saber/Local-Probe/internal/operations"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "local-probe-operations:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	f := flag.NewFlagSet("local-probe-operations", flag.ContinueOnError)
	root := f.String("journal", "", "absolute private operation journal, outside all workspaces")
	action := f.String("action", "list", "init, list, inspect, approve, reject, cancel or before")
	id := f.String("id", "", "operation ID")
	expected := f.String("digest", "", "exact plan digest shown by inspect")
	ack := f.Bool("ack-trusted-host", false, "acknowledge current-user file/network access; not a sandbox")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	j, err := operations.OpenJournal(*root, *action == "init")
	if err != nil {
		return err
	}
	defer j.Close()
	if *action == "init" {
		return j.Initialize()
	}
	switch *action {
	case "cancel":
		return j.CancelLocal(*id, *expected)
	case "before":
		b, err := j.BeforeImage(*id, *expected)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Data []byte `json:"data_base64"`
		}{b})
	case "approve", "reject":
		return j.Decide(*id, *expected, *action == "approve", *ack)
	case "list", "inspect":
		records, err := j.Records()
		if err != nil {
			return err
		}
		if *action == "inspect" {
			r, ok := records[*id]
			if !ok {
				return operations.ErrDenied
			}
			return json.NewEncoder(os.Stdout).Encode(r)
		}
		out := []operations.Summary{}
		for _, r := range records {
			out = append(out, r.Summary)
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	default:
		return errors.New("unsupported action")
	}
}
