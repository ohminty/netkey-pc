//go:build !windows

package main

import (
	"errors"
	"log"
)

func runService(dir string) { log.Fatal("the service mode is Windows only; use netkeyd run") }

func lockAdapter(logf func(string, ...any)) {}

func setupCmd(cmd, dir, driverDir string) error {
	return errors.New("install and uninstall are Windows only")
}
