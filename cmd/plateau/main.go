package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/creatrip/plateau/internal/adapter/cli"
	"github.com/creatrip/plateau/internal/adapter/hostlock"
	"github.com/creatrip/plateau/internal/adapter/incus"
	"github.com/creatrip/plateau/internal/adapter/lima"
	"github.com/creatrip/plateau/internal/application"
)

var version = "dev"

func main() {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "initialize Plateau: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	locks := hostlock.Directory{Path: filepath.Join(configDirectory, "plateau", "locks"), Context: ctx}
	host := lima.NewHost(ctx, os.Stdout, os.Stderr)
	manager := incus.NewManager(ctx, host, locks, os.Stdout, os.Stderr)
	instances := application.NewInstances(manager, locks)
	bootstrap := application.NewBootstrap(locks, lima.NewInstaller(ctx, os.Stdout), host)
	code := cli.New(version, bootstrap, instances, os.Stdout, os.Stderr).Run(os.Args[1:])
	cancel()
	os.Exit(code)
}
