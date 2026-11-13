package httpapi

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

func TestRoutingListenerSuggestions(t *testing.T) {
	config := telemt.ConfigSections{
		"server": json.RawMessage(`{"listeners":[{"ip":"0.0.0.0","port":51974},{"ip":"::","port":51974},
			{"ip":"192.0.2.3","port":51974,"transport":"mtproxy"},{"ip":"127.0.0.1","port":80,"transport":"web"},
			{"ip":"10.1.2.3","port":18080,"transport":"web"},{"ip":"10.1.2.3","transport":"web"}]}`),
		"web": json.RawMessage(`{"enabled":true}`),
	}
	native, web := routingListenerAddresses(config, "https://192.0.2.3:9443")
	if !slices.Equal(native, []string{"192.0.2.3:51974"}) || !slices.Equal(web, []string{"10.1.2.3:18080"}) {
		t.Fatalf("native %v, web %v", native, web)
	}
	config["web"] = json.RawMessage(`{"enabled":false}`)
	native, web = routingListenerAddresses(config, "https://[2001:db8::1]:9443")
	if !slices.Equal(native, []string{"192.0.2.3:51974", "[2001:db8::1]:51974"}) || len(web) != 0 {
		t.Fatalf("native %v, disabled web %v", native, web)
	}
}
