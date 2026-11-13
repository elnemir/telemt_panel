package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

type routingNodeSuggestion struct {
	NodeID string   `json:"node_id"`
	Native []string `json:"native"`
	Web    []string `json:"web"`
	Status string   `json:"status"`
}

func (s *Server) handleClusterRoutingSuggestions(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) || !s.requireNodes(w) {
		return
	}
	id := r.PathValue("cluster_id")
	if _, err := s.platform.GetCluster(r.Context(), id); err != nil {
		writePlatformError(w, err)
		return
	}
	members, err := s.platform.ListMembers(r.Context(), id)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := make([]routingNodeSuggestion, 0, len(members))
	for _, member := range members {
		if member.State == "active" {
			out = append(out, routingNodeSuggestion{NodeID: member.NodeID, Native: []string{}, Web: []string{}, Status: "unavailable"})
		}
	}
	// Bound concurrent upstream reads even when the cluster has many members.
	var work sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i := range out {
		work.Go(func() {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			node, client, err := s.nodes.Resolve(out[i].NodeID)
			if err != nil {
				return
			}
			sections, _, err := client.GetConfig(ctx)
			if err == nil {
				out[i].Native, out[i].Web = routingListenerAddresses(sections, node.APIURL)
				out[i].Status = "read"
			}
		})
	}
	work.Wait()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func routingListenerAddresses(sections telemt.ConfigSections, apiURL string) ([]string, []string) {
	native, web := []string{}, []string{}
	var server struct {
		Listeners []struct {
			IP        netip.Addr `json:"ip"`
			Port      uint16     `json:"port"`
			Transport string     `json:"transport"`
		} `json:"listeners"`
	}
	if json.Unmarshal(sections["server"], &server) != nil {
		return native, web
	}
	var host netip.Addr
	if u, err := url.Parse(apiURL); err == nil {
		host, _ = netip.ParseAddr(u.Hostname())
		host = host.Unmap()
	}
	var webConfig struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.Unmarshal(sections["web"], &webConfig)
	for _, listener := range server.Listeners {
		ip := listener.IP.Unmap()
		if ip.IsUnspecified() && ip.Is4() == host.Is4() {
			ip = host
		}
		if listener.Port == 0 || !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.Zone() != "" {
			continue
		}
		address := netip.AddrPortFrom(ip, listener.Port).String()
		switch listener.Transport {
		case "", "mtproxy":
			native = append(native, address)
		case "web":
			if webConfig.Enabled {
				web = append(web, address)
			}
		}
	}
	slices.Sort(native)
	slices.Sort(web)
	return slices.Compact(native), slices.Compact(web)
}
