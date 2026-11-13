//go:build !lite

package nodeinstall

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	ingressbundle "github.com/amirotin/telemt_panel/deploy/ingress"
	"golang.org/x/crypto/ssh"
)

func (i *Installer) InstallIngress(ctx context.Context, request IngressRequest, config, clusterID string, revision int64, current func(context.Context) bool, report func(string)) (int, error) {
	target, err := request.target()
	if err != nil || !ingressClusterPattern.MatchString(clusterID) || revision < 1 {
		return 0, ErrInvalid
	}
	client, closeSocket, err := i.connect(ctx, target)
	if err != nil {
		return 0, err
	}
	defer closeSocket()

	config = fmt.Sprintf("# Telemt cluster: %s\n# Cluster revision: %d\n%s", clusterID, revision, config)
	stage, cleanup, err := transferBundle(client, bundle{files: ingressbundle.Files(config)})
	if err != nil {
		return 0, err
	}
	defer cleanup()
	command := "/bin/sh " + shellQuote(stage+"/install.sh")
	if !current(ctx) {
		return 0, ErrIngressStale
	}
	report("packages")
	if _, err := smallCommand(client, command+" packages >/dev/null", nil); err != nil {
		return 0, ingressRemoteError(err, ErrIngressPackages)
	}

	report("validating")
	capacity := 0
	transaction, err := beginRemoteHandshake(client, command+" apply-managed", func(line string) bool {
		value, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "ready ")
		capacity, _ = strconv.Atoi(value)
		return ok && capacity >= 256 && capacity <= 100000
	})
	if err != nil {
		return 0, ingressRemoteError(err, ErrApply)
	}
	if !current(ctx) {
		_ = transaction.finish(false)
		return 0, ErrIngressStale
	}
	report("activating")
	if err := transaction.finish(true); err != nil {
		return 0, ingressRemoteError(err, ErrUnconfirmed)
	}
	return capacity, nil
}

func ingressRemoteError(err, fallback error) error {
	if errors.Is(err, ErrUnconfirmed) {
		return ErrUnconfirmed
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitStatus() {
		case 11:
			return ErrUnsupported
		case 12:
			return ErrIngressConflict
		case 13:
			return ErrIngressConfig
		case 14:
			return ErrIngressCapacity
		case 24:
			return ErrIngressRollback
		case 25:
			return ErrUnconfirmed
		}
	}
	return fallback
}
