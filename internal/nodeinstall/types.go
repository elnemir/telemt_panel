package nodeinstall

import (
	"net/netip"
	"regexp"
	"strings"
)

const Release = "3.5.17"

var userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`)
var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
var ingressClusterPattern = regexp.MustCompile(`^c_[a-f0-9]{32}$`)
var requestIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func validPort(port int) bool { return port >= 1 && port <= 65535 }

type Request struct {
	Name            string `json:"name"`
	Host            string `json:"host"`
	SSHPort         int    `json:"ssh_port"`
	SSHUser         string `json:"ssh_user"`
	HostFingerprint string `json:"host_fingerprint"`
	ProxyPort       int    `json:"proxy_port"`
	ManagementPort  int    `json:"management_port"`
	WithAgent       bool   `json:"with_agent"`
}

type Plan struct {
	Request
	Architecture string `json:"architecture"`
	PanelPeer    string `json:"panel_peer"`
	Release      string `json:"release"`
	APIURL       string `json:"api_url"`
	ConfigFiles  bool   `json:"config_files,omitempty"`
}

type IngressRequest struct {
	Host            string `json:"host"`
	SSHPort         int    `json:"ssh_port"`
	SSHUser         string `json:"ssh_user"`
	HostFingerprint string `json:"host_fingerprint"`
	RequestID       string `json:"request_id"`
}

func (r IngressRequest) target() (Request, error) {
	host, err := netip.ParseAddr(strings.TrimSpace(r.Host))
	if err != nil || host.Zone() != "" || !host.IsGlobalUnicast() || !validPort(r.SSHPort) ||
		!userPattern.MatchString(r.SSHUser) || !fingerprintPattern.MatchString(r.HostFingerprint) || !requestIDPattern.MatchString(r.RequestID) {
		return Request{}, ErrInvalid
	}
	return Request{Host: host.Unmap().String(), SSHPort: r.SSHPort, SSHUser: r.SSHUser, HostFingerprint: r.HostFingerprint}, nil
}

func (r IngressRequest) Valid() bool {
	_, err := r.target()
	return err == nil
}

type UpgradeRequest struct {
	SSHPort          int    `json:"ssh_port"`
	SSHUser          string `json:"ssh_user"`
	HostFingerprint  string `json:"host_fingerprint"`
	Telemt           bool   `json:"telemt"`
	Agent            bool   `json:"agent"`
	EnableManagement bool   `json:"enable_management,omitempty"`
	RequestID        string `json:"request_id"`
}

const (
	UpgradeUpdated    = "updated"
	UpgradeUnchanged  = "unchanged"
	UpgradeRolledBack = "rolled_back"
	UpgradeFailed     = "failed"
	UpgradeUnknown    = "unconfirmed"
)

func (r UpgradeRequest) Valid() bool {
	return (r.Telemt || r.Agent) && (!r.EnableManagement || r.Telemt && r.Agent) && validPort(r.SSHPort) && userPattern.MatchString(r.SSHUser) &&
		fingerprintPattern.MatchString(r.HostFingerprint) && requestIDPattern.MatchString(r.RequestID)
}

type failure string

func (e failure) Error() string { return string(e) }

const (
	ErrInvalid         failure = "node_install_invalid"
	ErrSSH             failure = "node_install_ssh"
	ErrHostKey         failure = "node_install_host_key"
	ErrUnsupported     failure = "node_install_unsupported"
	ErrExisting        failure = "node_install_existing"
	ErrPorts           failure = "node_install_ports"
	ErrArtifacts       failure = "node_install_artifacts"
	ErrFullArtifact    failure = "node_install_full_artifact_required"
	ErrApply           failure = "node_install_apply"
	ErrHealth          failure = "node_install_health"
	ErrTLSActivity     failure = "node_install_tls_activity"
	ErrManagement      failure = "node_install_management"
	ErrRegistry        failure = "node_install_registry"
	ErrUnconfirmed     failure = "node_install_unconfirmed"
	ErrIngressStale    failure = "ingress_install_stale"
	ErrIngressPackages failure = "ingress_install_packages"
	ErrIngressConflict failure = "ingress_install_conflict"
	ErrIngressConfig   failure = "ingress_install_config"
	ErrIngressCapacity failure = "ingress_install_capacity"
	ErrIngressRollback failure = "ingress_install_rollback"
	ErrUpgradeStart    failure = "node_upgrade_services"
	ErrUpgradeCheck    failure = "node_upgrade_verify"
)
