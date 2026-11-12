//go:build linux && !lite

package nodeinstall

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeTransactionKeepsConfigAndRestoresPreviousFiles(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("Linux shell fixture requires flock")
	}
	const configText = "# retained comment\n[server]\nport = 51974\n"
	for _, scenario := range []struct {
		name, decision, fault string
		exit                  int
		upgraded              bool
	}{
		{name: "commit", decision: "commit\n", upgraded: true},
		{name: "abort after API failure", decision: "abort\n"},
		{name: "SSH disconnect", exit: 24},
		{name: "TOML drift during startup", decision: "commit\n", fault: "config", exit: 24},
		{name: "failed service startup", fault: "service", exit: 24},
		{name: "failed restore keeps interlock", fault: "restore", exit: 25},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "node")
			stage := filepath.Join(dir, "test.0123456789")
			bin := filepath.Join(dir, "bin")
			for _, path := range []string{root, stage, bin} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(root, 0755); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(dir, "telemt.toml")
			put := func(path, content string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			}
			expect := func(path, content string) {
				t.Helper()
				actual, err := os.ReadFile(path)
				if err != nil || string(actual) != content {
					t.Fatalf("%s: got %q (%v), want %q", filepath.Base(path), actual, err, content)
				}
			}
			put(config, configText, 0600)
			put(filepath.Join(root, "telemt"), "current-proxy", 0755)
			put(filepath.Join(root, "node-runtime"), "old-agent", 0755)
			put(filepath.Join(stage, "node-runtime"), "new-agent", 0700)
			put(filepath.Join(stage, "checksums"), fmt.Sprintf("%x  node-runtime\n", sha256.Sum256([]byte("new-agent"))), 0600)
			put(filepath.Join(bin, "id"), "#!/bin/sh\necho 0\n", 0755)
			put(filepath.Join(bin, "systemctl"), `#!/bin/bash
set -eu
case "$1" in
    show)
        case "$4" in
            FragmentPath) printf '/etc/systemd/system/%s\n' "$2" ;;
            DropInPaths) ;;
            ExecStart)
                binary=node-runtime
                if [ "$2" = telemt.service ]; then binary=telemt; fi
                printf '{ path=%s/%s ; argv[]=... }\n' "$ROOT" "$binary"
                ;;
        esac
        ;;
    is-active) ;;
    reset-failed) rm -f "$TRACE.blocked" ;;
    stop|start)
        echo "$*" >> "$TRACE"
        if [ "$1" = start ]; then
            if [ -e "$TRACE.blocked" ]; then exit 1; fi
            if [ "$FAULT" = restore ]; then exit 1; fi
            if [ "$(cat "$ROOT/node-runtime")" = new-agent ]; then
                case "$FAULT" in
                    config) printf 'unexpected drift\n' > "$CONFIG" ;;
                    service) touch "$TRACE.blocked"; exit 1 ;;
                esac
            fi
        fi
        ;;
    *) exit 1 ;;
esac
`, 0755)
			arch, err := exec.Command("uname", "-m").Output()
			if err != nil {
				t.Fatal(err)
			}
			script := strings.NewReplacer(
				"/opt/telemt-node", root,
				installedConfigPath, config,
				"/run/telemt-node-install.lock", filepath.Join(dir, "lock"),
				"export PATH=", "export PATH="+shellQuote(bin)+":",
				"0:755", fmt.Sprintf("%d:755", os.Getuid()),
			).Replace(upgradeScript)
			trace := filepath.Join(dir, "trace")
			cmd := exec.Command("bash", "-c", script, "--", stage, strings.TrimSpace(string(arch)))
			cmd.Env = append(os.Environ(), "ROOT="+root, "CONFIG="+config, "FAULT="+scenario.fault, "TRACE="+trace)
			cmd.Stdin = strings.NewReader(scenario.decision)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if (scenario.exit == 0 && err != nil) ||
				(scenario.exit != 0 && (!errors.As(err, &exit) || exit.ExitCode() != scenario.exit)) {
				t.Fatalf("remote result: %v; stdout/stderr: %s", err, out)
			}
			expect(config, configText)
			expect(filepath.Join(root, "telemt"), "current-proxy")
			pending := filepath.Join(root, ".panel-upgrade-pending")
			if scenario.exit == 25 {
				expect(filepath.Join(pending, "node-runtime"), "old-agent")
				expect(filepath.Join(pending, "telemt.toml"), configText)
				return
			}
			agent := "old-agent"
			if scenario.upgraded {
				agent = "new-agent"
			}
			expect(filepath.Join(root, "node-runtime"), agent)
			if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("completed transaction kept pending marker: %v", err)
			}
			backup := filepath.Join(root, "upgrade-backup.0123456789")
			expect(filepath.Join(backup, "telemt.toml"), configText)
			expect(filepath.Join(backup, "node-runtime"), "old-agent")
			calls, err := os.ReadFile(trace)
			if err != nil || (scenario.fault != "config" && strings.Contains(string(calls), " telemt.service")) {
				t.Fatalf("unexpected service calls: %s (%v)", calls, err)
			}
		})
	}
}
