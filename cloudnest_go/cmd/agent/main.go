// cloudnest-agent is a foreground process supervisor for official frpc.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"cloudenest_go/internal/frpagent"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "__supervise-frpc" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := frpagent.SupervisorMain(ctx, os.Args[2])
		cancel()
		os.Exit(code)
	}
	if e := run(os.Args[1:]); e != nil && !errors.Is(e, context.Canceled) {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) > 0 && args[0] == "launch-agent" {
		return launchAgent(args[1:], os.Stdout)
	}
	if len(args) == 0 || (args[0] != "enroll" && args[0] != "run" && args[0] != "protect-state") {
		return errors.New("usage: cloudnest-agent enroll|run|protect-state|launch-agent --state /absolute/private/directory (see docs/remediation/FRP-AGENT.md)")
	}
	f := flag.NewFlagSet("cloudnest-agent", flag.ContinueOnError)
	state := f.String("state", "", "absolute private state directory")
	endpoint := f.String("server", "", "HTTPS CloudNest origin (enroll only)")
	ca := f.String("ca", "", "CloudNest CA PEM file; default system trust (enroll only)")
	bin := f.String("frpc", "", "official 0.71.1 frpc binary (enroll only)")
	frps := f.String("frps-host", "", "locally authorized FRP server (enroll only)")
	frpsPort := f.Int("frps-port", 7000, "FRP control port (enroll only)")
	frpsCA := f.String("frps-ca", "", "trusted FRP CA PEM; required for non-loopback server")
	frpsName := f.String("frps-name", "", "FRP certificate server name")
	ports := f.String("local-ports", "", "comma-separated allowed loopback upstream ports (enroll only)")
	keyFile := f.String("key-file", "", "private 32-byte key file outside state directory; required on Linux")
	keychain := f.String("keychain", "", "optional macOS keychain path; otherwise user's default keychain")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	s, e := frpagent.OpenStore(*state)
	if e != nil {
		return e
	}
	defer s.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	options := frpagent.VaultOptions{KeyFile: *keyFile, Keychain: *keychain}
	if args[0] == "protect-state" {
		bad := false
		f.Visit(func(v *flag.Flag) {
			if v.Name != "state" && v.Name != "key-file" && v.Name != "keychain" {
				bad = true
			}
		})
		if bad {
			return errors.New("protect-state accepts only state and key storage flags")
		}
		if e = frpagent.ProtectState(s, options); e != nil {
			return e
		}
		fmt.Fprintln(os.Stdout, "Agent persistent identity and journal protected; existing backups are unchanged.")
		return nil
	}
	if args[0] == "enroll" {
		if _, e = s.Read("identity.json", (4<<20)+64); !errors.Is(e, os.ErrNotExist) {
			return errors.New("enrollment requires a new empty state directory")
		}
		if e = frpagent.UnlockVault(s, options, true); e != nil {
			return e
		}
		p := frpagent.Policy{ServerAddr: *frps, ServerPort: *frpsPort, ServerName: *frpsName}
		for _, v := range strings.Split(*ports, ",") {
			n, e := strconv.Atoi(v)
			if e != nil {
				return frpagent.ErrConfig
			}
			p.LocalPorts = append(p.LocalPorts, n)
		}
		if *frpsCA != "" {
			b, e := readCA(*frpsCA)
			if e != nil {
				return e
			}
			if e = s.Write("frps-ca.pem", b, 0600); e != nil {
				return e
			}
			p.TrustedCAFile = filepath.Join(s.Path, "frps-ca.pem")
		}
		if e = p.Validate(); e != nil {
			return e
		}
		if e = frpagent.InstallBinary(s, *bin); e != nil {
			return e
		}
		var caPEM []byte
		if *ca != "" {
			caPEM, e = readCA(*ca)
			if e != nil {
				return e
			}
		}
		client, e := frpagent.NewClient(*endpoint, "", caPEM)
		if e != nil {
			return e
		}
		defer client.Close()
		fmt.Fprintln(os.Stderr, "Read one-time enrollment token from stdin; do not put it in command arguments.")
		raw, e := io.ReadAll(io.LimitReader(os.Stdin, 256))
		if e != nil || len(raw) >= 256 {
			return errors.New("invalid enrollment input")
		}
		registration, e := client.Enroll(ctx, strings.TrimSpace(string(raw)))
		if e != nil {
			return e
		}
		if e = frpagent.SaveIdentity(s, frpagent.Identity{Version: 1, Endpoint: *endpoint, Registration: registration, Policy: p, ControlCA: caPEM}); e != nil {
			return errors.New("credential could not be saved; issue a new enrollment before retrying")
		}
		fmt.Fprintf(os.Stdout, "Device %d enrolled; frpc %s verified.\n", registration.DeviceID, frpagent.FRPVersion)
		return nil
	}
	// Prevent misleading run-time flags: authorization only comes from enrollment.
	badFlag := false
	f.Visit(func(v *flag.Flag) {
		if v.Name != "state" {
			badFlag = true
		}
	})
	if badFlag {
		return errors.New("run accepts only --state")
	}
	if e = frpagent.UnlockVault(s, frpagent.VaultOptions{}, false); e != nil {
		return e
	}
	id, e := frpagent.LoadIdentity(s)
	if e != nil {
		return e
	}
	for _, name := range []string{"active.json", "candidate.json"} {
		if e = s.RemoveRuntime(name); e != nil {
			return e
		}
	}
	client, e := frpagent.NewClient(id.Endpoint, id.Registration.Credential, id.ControlCA)
	if e != nil {
		return e
	}
	defer client.Close()
	runner, e := frpagent.NewRunner(s, client, id)
	if e != nil {
		return e
	}
	runner.Report = func(e error) { fmt.Fprintln(os.Stderr, e) }
	fmt.Fprintln(os.Stdout, "CloudNest Agent running; credentials and frpc output are not logged.")
	return runner.Run(ctx)
}
func readCA(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("CA file unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(b) > 1<<20 || len(b) == 0 {
		return nil, errors.New("invalid CA file")
	}
	return b, nil
}
