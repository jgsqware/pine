package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// serviceName is the systemd unit Pine installs for itself.
const serviceName = "pine.service"

// cmdService manages a systemd (user) service that runs `pine serve` in the
// background — the same setup you'd otherwise wire up by hand.
func cmdService(args []string) {
	if len(args) == 0 {
		serviceUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "install":
		serviceInstall(args[1:])
	case "uninstall", "remove":
		serviceUninstall(args[1:])
	case "status":
		serviceStatus()
	default:
		serviceUsage()
		os.Exit(2)
	}
}

func serviceUsage() {
	fmt.Fprint(os.Stderr, `Usage:
  pine service install [--addr :8743] [--data DIR] [--demo]   Install & start the systemd user service
  pine service status                                         Show the service status
  pine service uninstall                                      Stop & remove the service
`)
}

// unitPath returns ~/.config/systemd/user/pine.service.
func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceName), nil
}

// requireSystemd fails early on platforms without a usable systemctl.
func requireSystemd() {
	if runtime.GOOS != "linux" {
		fatalf("pine service manages a systemd unit, which is Linux-only (this is %s).\n"+
			"Run `pine serve` directly, or set up your platform's service manager manually.", runtime.GOOS)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		fatalf("systemctl not found on PATH — this command needs systemd.")
	}
}

func serviceInstall(args []string) {
	fs := flag.NewFlagSet("service install", flag.ExitOnError)
	addr := fs.String("addr", ":8743", "listen address")
	data := fs.String("data", defaultDataDir(), "data directory")
	demo := fs.Bool("demo", false, "register the bundled demo repository")
	_ = fs.Parse(args)
	requireSystemd()

	exe, err := os.Executable()
	if err != nil {
		fatalf("locate pine binary: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dataAbs, err := filepath.Abs(*data)
	if err != nil {
		fatalf("resolve data dir: %v", err)
	}

	path, err := unitPath()
	if err != nil {
		fatalf("locate unit path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatalf("create unit dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(unitFile(exe, *addr, dataAbs, *demo)), 0o644); err != nil {
		fatalf("write unit file: %v", err)
	}
	fmt.Printf("wrote %s\n", path)

	mustSystemctl("daemon-reload")
	mustSystemctl("enable", serviceName)
	// restart (not just start) so a re-install picks up the new unit and the
	// freshly installed binary even when the service was already running.
	mustSystemctl("restart", serviceName)

	// Linger lets the service run at boot without an interactive login. It needs
	// privileges, so treat failure as advisory rather than fatal.
	if u, err := user.Current(); err == nil {
		if err := exec.Command("loginctl", "enable-linger", u.Username).Run(); err != nil {
			fmt.Printf("\nNote: could not enable linger (needs root). To run Pine at boot before login:\n"+
				"  sudo loginctl enable-linger %s\n", u.Username)
		}
	}

	fmt.Printf("\n✓ Pine is running as a service on http://localhost%s\n", *addr)
	fmt.Print("\nManage it with:\n" +
		"  systemctl --user status pine     # check\n" +
		"  systemctl --user restart pine    # restart (after upgrading the binary)\n" +
		"  journalctl --user -u pine -f     # follow logs\n" +
		"  pine attach                      # drive it from the terminal\n")
}

func serviceUninstall(args []string) {
	fs := flag.NewFlagSet("service uninstall", flag.ExitOnError)
	_ = fs.Parse(args)
	requireSystemd()

	// Best-effort stop+disable; ignore errors so a partial install still cleans.
	_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName).Run()

	path, err := unitPath()
	if err != nil {
		fatalf("locate unit path: %v", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fatalf("remove unit file: %v", err)
	}
	mustSystemctl("daemon-reload")
	fmt.Printf("✓ removed %s and stopped the service\n", serviceName)
	fmt.Println("Linger (if enabled) was left as-is: sudo loginctl disable-linger $USER to undo it.")
}

func serviceStatus() {
	requireSystemd()
	cmd := exec.Command("systemctl", "--user", "status", serviceName, "--no-pager")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// systemctl exits non-zero when the unit is inactive/missing; that's normal
	// output for `status`, so don't treat it as a hard error.
	_ = cmd.Run()
}

// unitFile renders the systemd unit. Paths are quoted so spaces are safe.
func unitFile(exe, addr, data string, demo bool) string {
	execStart := fmt.Sprintf("%q serve --addr %s --data %q", exe, addr, data)
	if demo {
		execStart += " --demo"
	}
	return fmt.Sprintf(`[Unit]
Description=Pine — Ansible control plane (web UI + API + scheduler)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5
Environment=HOME=%%h

[Install]
WantedBy=default.target
`, execStart)
}

// mustSystemctl runs `systemctl --user <args>` and aborts on failure.
func mustSystemctl(args ...string) {
	full := append([]string{"--user"}, args...)
	cmd := exec.Command("systemctl", full...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fatalf("systemctl %s: %v", strings.Join(args, " "), err)
	}
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "pine service: "+format+"\n", a...)
	os.Exit(1)
}
