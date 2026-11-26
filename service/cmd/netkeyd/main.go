// netkeyd is the NetKey PC app's service.
//
//	netkeyd service              run under the Windows service manager (the installer registers this)
//	netkeyd run                  run in the foreground (for testing)
//	netkeyd install DRIVERDIR    create the NetKey adapter, the state folder and the service (installer, as admin)
//	netkeyd uninstall DRIVERDIR  remove them again
//	netkeyd status | join CODE [NAME] | leave | test     talk to the running service
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"netkey.no/pc/internal/daemon"
)

func stateDir() string {
	if d := os.Getenv("NETKEY_STATE"); d != "" {
		return d
	}
	if d := os.Getenv("ProgramData"); d != "" {
		return filepath.Join(d, "NetKey")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".netkey-pc")
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: netkeyd service | run | install DRIVERDIR | uninstall DRIVERDIR | status | "+
		"join CODE [NAME] | leave | test")
	os.Exit(2)
}

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		usage()
	}
	dir := stateDir()
	switch os.Args[1] {
	case "service":
		runService(dir)
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := serve(ctx, dir); err != nil {
			log.Fatal(err)
		}
	case "name-adapter":
		if err := setupCmd("name-adapter", dir, ""); err != nil {
			log.Fatal(err)
		}
	case "install", "uninstall":
		if len(os.Args) < 3 {
			usage()
		}
		if err := setupCmd(os.Args[1], dir, os.Args[2]); err != nil {
			log.Fatal(err)
		}
	case "status", "leave", "test", "join":
		req := map[string]any{"op": os.Args[1]}
		if os.Args[1] == "join" {
			if len(os.Args) < 3 {
				usage()
			}
			req["code"] = os.Args[2]
			if len(os.Args) > 3 {
				req["name"] = os.Args[3]
			}
		}
		c, err := daemon.Dial(dir)
		if err != nil {
			log.Fatalf("the NetKey service isn't running: %v", err)
		}
		defer c.Close()
		r, err := daemon.Client(c, req)
		if err != nil {
			log.Fatal(err)
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		if r["ok"] != true {
			os.Exit(1)
		}
	default:
		usage()
	}
}

// serve runs the daemon until ctx ends.
func serve(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logf := daemon.NewLogger(dir)
	logf("NetKey service %s starting", daemon.Version)
	go lockAdapter(logf) // PowerShell takes a few seconds; the pipe opens meanwhile
	d, err := daemon.New(dir, logf)
	if err != nil {
		return err
	}
	l, err := daemon.Listen(dir)
	if err != nil {
		return fmt.Errorf("control channel: %w", err)
	}
	d.Run(ctx, l)
	logf("NetKey service stopped")
	return nil
}
