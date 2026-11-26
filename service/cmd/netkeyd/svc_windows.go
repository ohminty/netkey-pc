//go:build windows

package main

import (
	"context"
	"log"
	"os"

	"golang.org/x/sys/windows/svc"

	"netkey.no/pc/internal/setup"
)

type handler struct{ dir string }

func (h handler) Execute(args []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, h.dir) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			cancel()
			if err != nil {
				log.Print(err)
				return false, 1
			}
			return false, 0
		}
	}
}

func runService(dir string) {
	if err := svc.Run(setup.ServiceName, handler{dir}); err != nil {
		log.Fatal(err)
	}
}

func lockAdapter(logf func(string, ...any)) {
	if !setup.AdapterExists() {
		logf("the NetKey adapter is missing: reinstall NetKey")
		return
	}
	if err := setup.LockAdapter(logf); err != nil {
		logf("%v", err)
	}
}

func setupCmd(cmd, dir, driverDir string) error {
	logf := func(f string, a ...any) { log.Printf(f, a...) }
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if cmd == "name-adapter" {
		return setup.NameDevice(logf)
	}
	if cmd == "uninstall" {
		if err := setup.RemoveService(); err != nil {
			logf("service: %v", err)
		}
		return setup.RemoveAdapter(driverDir, logf)
	}
	if err := setup.InstallAdapter(driverDir, logf); err != nil {
		return err
	}
	if err := setup.PrepareState(dir); err != nil {
		return err
	}
	if err := setup.InstallService(exe); err != nil {
		return err
	}
	logf("NetKey installed and started")
	return nil
}
