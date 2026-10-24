// local-probe-pair is an OFFLINE owner-management tool. It has no listener and
// never prints private keys. Mutations require explicit local acknowledgements.
package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/peertransport"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "local-probe-pair:", err)
		os.Exit(1)
	}
}
func run(args []string, in io.Reader, out, stderr io.Writer) error {
	f := flag.NewFlagSet("local-probe-pair", flag.ContinueOnError)
	f.SetOutput(stderr)
	action := f.String("action", "show", "target-init, agent-init, request, issue, install, revoke, show")
	path := f.String("store", "", "explicit absolute private identity directory")
	expected := f.String("fingerprint", "", "independently reviewed public bundle/CSR fingerprint")
	connection := f.String("connection-id", "", "locally selected existing target connection")
	configPath := f.String("config", "", "absolute target config, mandatory for issue")
	keyPin := f.String("key-pin", "", "public Agent key pin to revoke")
	approve := f.Bool("approve", false, "local user approves the exact pairing/revocation")
	offline := f.Bool("offline", false, "all runtimes using this identity store are stopped")
	if err := f.Parse(args); err != nil {
		return err
	}
	read := func(value any) error {
		data, err := io.ReadAll(io.LimitReader(in, deviceidentity.MaxArtifactBytes+1))
		if err != nil {
			return deviceidentity.ErrInvalid
		}
		return deviceidentity.DecodePublic(data, value)
	}
	if f.NArg() == 0 && (*action == "fingerprint-request" || *action == "fingerprint-bundle") {
		var fingerprint string
		var err error
		if *action == "fingerprint-request" {
			var r deviceidentity.Request
			if err = read(&r); err == nil {
				fingerprint, err = r.Fingerprint()
			}
		} else {
			var b deviceidentity.Bundle
			if err = read(&b); err == nil {
				fingerprint, err = b.Fingerprint()
			}
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, fingerprint)
		return err
	}
	if f.NArg() != 0 || *path == "" {
		return errors.New("provide explicit -store and no positional arguments")
	}
	if *action == "target-init" || *action == "agent-init" {
		if !*approve || !*offline {
			return errors.New("initialization requires -approve -offline")
		}
		s, err := deviceidentity.CreateStore(*path)
		if err != nil {
			return err
		}
		defer s.Close()
		if *action == "target-init" {
			a, err := deviceidentity.NewAuthority()
			if err != nil {
				return err
			}
			if err = s.SaveAuthority(a); err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(a.Bundle())
		}
		a, r, err := deviceidentity.NewAgent()
		if err != nil {
			return err
		}
		if err = s.SaveAgent(a); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(r)
	}
	s, err := deviceidentity.OpenStore(*path)
	if err != nil {
		return err
	}
	defer s.Close()
	switch *action {
	case "request":
		a, err := s.LoadAgent()
		if err != nil {
			return err
		}
		r, err := a.Request()
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(r)
	case "show":
		a, err := s.LoadAuthority()
		if err == nil {
			return json.NewEncoder(out).Encode(a.Bundle())
		}
		p, err := s.LoadPairing()
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(p)
	case "issue":
		if !*approve || !*offline || *expected == "" || *configPath == "" || *connection == "" {
			return errors.New("issue requires -approve -offline -fingerprint -config -connection-id")
		}
		store, err := config.NewFileStore(*configPath)
		if err != nil {
			return errors.New("invalid target config")
		}
		snapshot, err := store.Load()
		if err != nil {
			return errors.New("target config unavailable")
		}
		c, ok := snapshot.Config().Connection(*connection)
		if !ok || !c.Enabled() || c.Purpose() != config.PurposeAgentRemote {
			return errors.New("select an enabled agent_remote connection")
		}
		var r deviceidentity.Request
		if err := read(&r); err != nil {
			return err
		}
		g, err := s.IssueGrant(r, *expected, *connection, 30*24*time.Hour)
		if err != nil {
			return err
		}
		a, err := s.LoadAuthority()
		if err != nil {
			return err
		}
		b := a.Bundle()
		fingerprint, err := b.Fingerprint()
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(deviceidentity.Pairing{Version: deviceidentity.Version, Bundle: b, Grant: g, Fingerprint: fingerprint})
	case "install":
		if !*approve || !*offline || *expected == "" {
			return errors.New("install requires -approve -offline -fingerprint")
		}
		var p deviceidentity.Pairing
		if err := read(&p); err != nil {
			return err
		}
		return s.InstallPairing(p, *expected)
	case "revoke":
		if !*approve || !*offline {
			return errors.New("offline revocation requires -approve -offline")
		}
		b, err := hex.DecodeString(*keyPin)
		if err != nil || len(b) != 32 {
			return deviceidentity.ErrInvalid
		}
		var pin peertransport.Pin
		copy(pin[:], b)
		return s.Revoke(pin)
	default:
		return errors.New("unsupported pairing action")
	}
}
