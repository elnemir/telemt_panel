//go:build !lite

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/netip"
	"os"
	"os/signal"
	"syscall"

	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/httpapi"
	"github.com/amirotin/telemt_panel/internal/nodeinstall"
)

const nodeInstallSupported = true

func openNodeInstaller(cfg config.NodeInstallConfig) (httpapi.NodeInstaller, error) {
	return nodeinstall.Open(cfg.SSHKeyFile, nodeinstall.FileBinary{Path: cfg.TelemtBinaryFile, SHA256: cfg.TelemtBinarySHA, Version: cfg.TelemtVersion})
}

func runNodeGateway(args []string) error {
	flags := flag.NewFlagSet("node-gateway", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "", "management address")
	peer := flags.String("peer", "", "panel peer IP")
	cert := flags.String("cert", "", "server certificate")
	key := flags.String("key", "", "server private key")
	agent := flags.Bool("agent", false, "enable loopback agent proxy")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *listen == "" || *cert == "" || *key == "" {
		return errors.New("invalid node gateway configuration")
	}
	allowedPeer, err := netip.ParseAddr(*peer)
	if err != nil {
		return errors.New("invalid node gateway peer address")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return nodeinstall.RunGateway(ctx, *listen, *cert, *key, allowedPeer, *agent)
}
