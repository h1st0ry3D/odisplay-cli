//go:build darwin

package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Programs are resolved once, at startup, from the places Homebrew and a
// manual install put them. A single hardcoded path is wrong on half of Macs:
// `brew install ddcctl` lands in /opt/homebrew/bin on Apple Silicon and
// /usr/local/bin on Intel, and `make install PREFIX=~/.local` puts mxswitch in
// ~/.local/bin.
//
// They are resolved to absolute paths here rather than left to PATH at exec
// time, so nothing in the session can change what a name resolves to later.
var (
	ddcctl   = mustFind("ddcctl", "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin")
	mxswitch = mustFind("mxswitch", "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/usr/local/sbin")
)

// mustFind returns the first candidate that exists and is executable, or the
// first candidate, so a missing program is reported by name at the point of use
// rather than as an empty path.
func mustFind(name string, dirs ...string) string {
	for _, dir := range dirs {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	if len(dirs) > 0 {
		return filepath.Join(dirs[0], name)
	}
	return name
}

// mxswitchTimeout is larger than a plain DDC call because it enumerates HID
// devices and resolves a feature index before it sends anything.
const mxswitchTimeout = 15 * time.Second

// Darwin is the macOS backend: ddcctl for the monitor, mxswitch for the
// devices.
//
// ddcctl's input source values are the same MCCS numbers ddcutil uses, written
// in decimal: DisplayPort-1 is 15, HDMI-1 is 17, USB-C is 27. One number is
// stored in the config and each backend writes it the way its program wants.
//
// mxswitch sends the HID++ ChangeHost command (feature 0x1814) directly, so it
// needs no Logitech software running, but macOS requires Input Monitoring
// permission for the binary.
//
// Unlike Solaar, mxswitch does not take a device name: it picks the device
// itself, preferring a mouse over a keyboard, and moves one per call. So the
// device count has to come from somewhere, and the config's device list is what
// says it. The names in that list are Solaar's and are not used here; only how
// many there are.
//
// That is also a limit worth knowing before relying on it: with both a keyboard
// and a mouse present, mxswitch sends the mouse on the first call and the
// keyboard on the second, whatever order these steps run in. So the mouse
// leaves first on macOS, the reverse of Linux.
type Darwin struct {
	// Devices is how many Logitech devices to move. Names are ignored.
	Devices int
}

func (Darwin) Platform() string { return "darwin" }

// Plan builds the steps. The monitor is always first.
func (d Darwin) Plan(display, vcp int, channel string) (Plan, error) {
	if err := validVCP(vcp); err != nil {
		return Plan{}, err
	}
	p := Plan{Platform: "darwin"}
	// -d is a one-based display number, -i the MCCS value in decimal. This is
	// kfix/ddcctl's own documented invocation: `ddcctl -d 1 -i 17`.
	p.Steps = append(p.Steps, Step{
		Bin:  ddcctl,
		Args: []string{"-d", strconv.Itoa(display), "-i", decVCP(vcp)},
	})
	if channel == "0" {
		return p, nil
	}
	// mxswitch takes the channel as a bare argument and accepts only 1, 2 or 3,
	// so 0 never reaches it: off means this loop does not run.
	if err := validChannel(channel); err != nil {
		return Plan{}, err
	}
	for range d.Devices {
		p.Steps = append(p.Steps, Step{Bin: mxswitch, Args: []string{channel}})
	}
	return p, nil
}

// Run executes one step. mxswitch gets the larger budget because it enumerates
// HID devices before it sends anything.
func (d Darwin) Run(ctx context.Context, s Step) error {
	timeout := ddcTimeout
	if s.Bin == mxswitch {
		timeout = mxswitchTimeout
	}
	_, err := runStep(ctx, s, timeout)
	return err
}

// New returns the macOS backend. The device names are Solaar's and mean nothing
// here, but how many there are says how many devices to move.
func New(devices []string) Backend {
	count := 0
	for _, d := range devices {
		if strings.TrimSpace(d) != "" {
			count++
		}
	}
	return Darwin{Devices: count}
}

// Detect lists what ddcctl can see. It reads the monitor's identity and changes
// nothing.
//
// ddcctl prints one line per display with its number in front, so the number is
// read rather than assumed.
func (d Darwin) Detect(ctx context.Context) ([]Display, error) {
	text, err := runStepRaw(ctx, Step{Bin: ddcctl, Args: []string{"-m"}}, ddcTimeout)
	if err != nil {
		return nil, fmt.Errorf("ddcctl -m: %w", err)
	}
	var out []Display
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		num, name, ok := strings.Cut(line, ")")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(num))
		if err != nil {
			continue
		}
		out = append(out, Display{
			Number: n,
			Name:   strings.TrimSpace(name),
			// ddcctl lists what it can see rather than what can do DDC, so
			// this cannot say no. Saying yes for a display that cannot switch
			// would send the user off chasing a problem doctor cannot name, so
			// Where is left to the actual switch to report.
			Usable: true,
		})
	}
	return out, nil
}
