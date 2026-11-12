//go:build !lite

package nodeinstall

import (
	"bufio"
	"context"
	"crypto/subtle"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

//go:embed preflight.sh
var preflightScript string

//go:embed install.sh
var installScript string

var userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`)
var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
var stagePattern = regexp.MustCompile(`^/var/tmp/telemt-install\.[a-zA-Z0-9]{10}$`)

type Installer struct {
	signer     ssh.Signer
	loadBinary func(context.Context, string, string) (string, error)
}

func Open(keyFile string) (*Installer, error) {
	file, err := os.Open(keyFile)
	if err != nil {
		return nil, errors.New("cannot open the node installation SSH key")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || (info.Mode().Perm()&0077 != 0 && !systemdCredentialKey(keyFile, info)) {
		return nil, errors.New("node installation SSH key must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, errors.New("cannot read the node installation SSH key")
	}
	defer clear(raw)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, errors.New("invalid node installation SSH key")
	}
	return &Installer{signer: signer, loadBinary: releaseBinary}, nil
}

func normalize(request Request) (Request, error) {
	request.Name = strings.TrimSpace(request.Name)
	host, err := netip.ParseAddr(strings.TrimSpace(request.Host))
	if err != nil || host.Zone() != "" || !host.IsGlobalUnicast() ||
		len(request.Name) == 0 || len(request.Name) > 128 || !userPattern.MatchString(request.SSHUser) ||
		!fingerprintPattern.MatchString(request.HostFingerprint) || !validPort(request.SSHPort) ||
		!validPort(request.ProxyPort) || !validPort(request.ManagementPort) || request.ManagementPort < 1024 {
		return Request{}, ErrInvalid
	}
	ports := []int{request.SSHPort, request.ProxyPort, request.ManagementPort, 9091, 9092}
	for i, port := range ports {
		for _, used := range ports[:i] {
			if port == used {
				return Request{}, ErrInvalid
			}
		}
	}
	request.Host = host.Unmap().String()
	return request, nil
}

func validPort(port int) bool { return port >= 1 && port <= 65535 }

func (i *Installer) connect(ctx context.Context, r Request) (*ssh.Client, func(), error) {
	endpoint := net.JoinHostPort(r.Host, strconv.Itoa(r.SSHPort))
	socket, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return nil, nil, ErrSSH
	}
	stop := context.AfterFunc(ctx, func() { _ = socket.Close() })
	closeSocket := func() { stop(); _ = socket.Close() }
	_ = socket.SetDeadline(time.Now().Add(10 * time.Second))
	incorrect := false
	conn, channels, requests, err := ssh.NewClientConn(socket, endpoint, &ssh.ClientConfig{
		User:              r.SSHUser,
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(i.signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(key)), []byte(r.HostFingerprint)) != 1 {
				incorrect = true
				return ErrHostKey
			}
			return nil
		},
	})
	if err != nil {
		closeSocket()
		if incorrect {
			return nil, nil, ErrHostKey
		}
		return nil, nil, ErrSSH
	}
	_ = socket.SetDeadline(time.Time{})
	return ssh.NewClient(conn, channels, requests), closeSocket, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func preflight(client *ssh.Client, r Request) (Plan, error) {
	args := fmt.Sprintf(" %d %d", r.ProxyPort, r.ManagementPort)
	lines, err := smallCommand(client, "/bin/bash -s --"+args, strings.NewReader(preflightScript))
	if err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitStatus() {
			case 11:
				return Plan{}, ErrUnsupported
			case 12:
				return Plan{}, ErrExisting
			case 13:
				return Plan{}, ErrPorts
			}
		}
		return Plan{}, ErrSSH
	}
	fields := strings.Fields(lines)
	if len(fields) != 2 || !supportsArchitecture(fields[0]) {
		return Plan{}, ErrUnsupported
	}
	peer, err := netip.ParseAddr(fields[1])
	if err != nil || peer.Zone() != "" {
		return Plan{}, ErrSSH
	}
	return Plan{Request: r, Architecture: fields[0], PanelPeer: peer.Unmap().String(), Release: Release,
		APIURL: "https://" + net.JoinHostPort(r.Host, strconv.Itoa(r.ManagementPort))}, nil
}

func (i *Installer) Preview(ctx context.Context, request Request) (Plan, error) {
	r, err := normalize(request)
	if err != nil {
		return Plan{}, err
	}
	client, closeSocket, err := i.connect(ctx, r)
	if err != nil {
		return Plan{}, err
	}
	defer closeSocket()
	return preflight(client, r)
}

func smallCommand(client *ssh.Client, command string, input io.Reader) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	session.Stdin = input
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", err
	}
	session.Stderr = io.Discard
	if err := session.Start(command); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(stdout, 1025))
	if err != nil || len(raw) > 1024 {
		return "", ErrSSH
	}
	return string(raw), session.Wait()
}

type installation struct {
	session *ssh.Session
	input   io.WriteCloser
}

func begin(client *ssh.Client, stage string, r Request) (*installation, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, ErrApply
	}
	input, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, ErrApply
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, ErrApply
	}
	session.Stderr = io.Discard
	command := fmt.Sprintf("/bin/bash -c %s -- %s %d %d", shellQuote(installScript), shellQuote(stage), r.ProxyPort, r.ManagementPort)
	if err := session.Start(command); err != nil {
		session.Close()
		return nil, ErrApply
	}
	runner := &installation{session: session, input: input}
	line, err := bufio.NewReader(io.LimitReader(stdout, 32)).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = input.Close()
		var exit *ssh.ExitError
		if waitErr := session.Wait(); !errors.As(waitErr, &exit) || exit.ExitStatus() == 25 {
			session.Close()
			return nil, ErrUnconfirmed
		}
		session.Close()
		return nil, ErrApply
	}
	return runner, nil
}

func (i *installation) finish(commit bool) error {
	defer i.session.Close()
	decision := "abort\n"
	if commit {
		decision = "commit\n"
	}
	if _, err := io.WriteString(i.input, decision); err != nil {
		return err
	}
	_ = i.input.Close()
	return i.session.Wait()
}
