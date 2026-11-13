// Package ingressbundle embeds the fixed installer and service files.
package ingressbundle

import _ "embed"

//go:embed install.sh
var installer string

//go:embed telemt-ingress.service
var service string

//go:embed telemt-ingress-reload
var reload string

func Files(config string) map[string]string {
	return map[string]string{
		"install.sh": installer, "telemt-ingress.service": service,
		"telemt-ingress-reload": reload, "haproxy.cfg": config,
	}
}
