//go:build lite

package main

import (
	"errors"

	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/httpapi"
)

const nodeInstallSupported = false

func openNodeInstaller(config.NodeInstallConfig) (httpapi.NodeInstaller, error) {
	return nil, errors.New("node installation requires the full panel build")
}

func runNodeGateway([]string) error {
	return errors.New("node gateway requires the full panel build")
}
