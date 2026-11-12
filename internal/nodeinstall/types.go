package nodeinstall

const Release = "3.5.17"

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
}

type failure string

func (e failure) Error() string { return string(e) }

const (
	ErrInvalid     failure = "node_install_invalid"
	ErrSSH         failure = "node_install_ssh"
	ErrHostKey     failure = "node_install_host_key"
	ErrUnsupported failure = "node_install_unsupported"
	ErrExisting    failure = "node_install_existing"
	ErrPorts       failure = "node_install_ports"
	ErrArtifacts   failure = "node_install_artifacts"
	ErrApply       failure = "node_install_apply"
	ErrHealth      failure = "node_install_health"
	ErrRegistry    failure = "node_install_registry"
	ErrUnconfirmed failure = "node_install_unconfirmed"
)
