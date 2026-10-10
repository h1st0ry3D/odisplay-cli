//go:build darwin

package backend

import (
	"context"
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
	ddcctl   = mustFind("ddcctl", searchDirs()...)
	mxswitch = mustFind("mxswitch", searchDirs()...)
)

// searchDirs is where both programs are looked for, in order.
//
// It has to include the user's own bin, because that is where a no-sudo install
// goes and it is where the instructions for mxswitch put it:
//
//	make install PREFIX="$HOME/.local"     ->  ~/.local/bin/mxswitch
//
// Leaving it out meant a program installed exactly as documented was reported
// as not installed at all, while the error named a different program again.
// The odisplay program itself is looked for in the same three places, so this
// is one rule rather than a second one that can drift.
func searchDirs() []string {
	dirs := []string{
		"/opt/homebrew/bin", // Homebrew on Apple Silicon
		"/usr/local/bin",    // Homebrew on Intel, and a sudo make install
		"/usr/bin",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return dirs
}

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

// ProbeLimit is how many display numbers Detect will try.
//
// ddcctl has no way to list displays. It takes a number that indexes the
// displays it can see, skipping the built-in one, and a number above that count
// is simply refused. So the list is found by asking each number in turn whether
// it answers, which is a read: `-i ?` queries the input source and writes
// nothing.
const ProbeLimit = 4

// ddcctl has no listing mode. There is no flag for it: the arguments are the
// settings to change, and `-d` only selects which display to change them on. An
// earlier version of this ran `ddcctl -m` expecting a list, which is the mute
// command, and hung until the timeout stopped it.
//
// So the displays are discovered by asking each number whether it answers.
// Three outcomes are told apart, because they need different fixes:
//
//	answers          the number works and the monitor speaks DDC
//	refused          no such number, so a lower one is right
//	no answer        the number may be right but the monitor is not replying
//
// The last is the Apple Silicon case, where some ports have no working DDC path
// at all, and it is worth saying so rather than reporting the number as wrong.
func (d Darwin) Detect(ctx context.Context) ([]Display, error) {
	var out []Display
	for n := 1; n <= ProbeLimit; n++ {
		text, err := runStepRaw(ctx, Step{
			Bin:  ddcctl,
			Args: []string{"-d", strconv.Itoa(n), "-i", "?"},
		}, probeTimeout)

		d := Display{Number: n, Name: "ddcctl display " + strconv.Itoa(n)}
		switch {
		case err == nil && strings.Contains(text, "0x60"):
			d.Usable = true
			d.Name = describeInput(text)
		case err != nil && strings.Contains(text+err.Error(), "Usage"):
			// ddcctl prints its help when the number is past the end of its
			// own list, so this number does not exist on this machine.
			d.Usable = false
			d.Why = "ddcctl has no display with this number"
		default:
			// The number may well be right; the monitor is what did not
			// answer. Saying it does not exist would send someone looking for
			// a number when the problem is elsewhere.
			//
			// Two causes, most likely first. A monitor usually only accepts
			// DDC on the input it is currently showing, so a machine whose
			// input is not on screen cannot command it: the channel is there
			// but the monitor is not listening on it. Only then is the port
			// worth suspecting.
			d.Usable = false
			d.Why = "the monitor did not answer. It usually only accepts DDC on " +
				"the input it is showing, so switch it to this machine's input " +
				"and look again. If it still does not answer, the port is the " +
				"likely cause: on Apple Silicon a built-in HDMI port often has " +
				"no working DDC path"
		}
		out = append(out, d)
	}
	return out, nil
}

// describeInput pulls the readable part out of a `-i ?` reply, so a display is
// labelled by the input it is on rather than by its number twice.
func describeInput(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Input Source") {
			return strings.Join(strings.Fields(line), " ")
		}
	}
	return "ddcctl display"
}
