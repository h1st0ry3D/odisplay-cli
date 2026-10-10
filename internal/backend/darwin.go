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
	m1ddc    = mustFind("m1ddc", searchDirs()...)
	ddcctl   = mustFind("ddcctl", searchDirs()...)
	mxswitch = mustFind("mxswitch", searchDirs()...)
)

// monitor is the program that moves the display, and it is a choice rather than
// a constant because the two are for different Macs.
//
// m1ddc is preferred. It is the one that works on Apple Silicon, which is every
// Mac made since 2020: ddcctl has an open issue from the M1 launch asking for
// this, its last commit is from November 2022, and the reason is that Apple
// replaced the framebuffer interfaces it drives. Its DDC calls do nothing on
// those machines while still reporting that they ran.
//
// ddcctl is kept as the fallback because it is the one that works on Intel
// Macs, where m1ddc explicitly does not.
func monitor() string {
	if Installed(m1ddc) {
		return m1ddc
	}
	if Installed(ddcctl) {
		return ddcctl
	}
	return ""
}

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
	if display < 1 {
		return Plan{}, fmt.Errorf("display %d is not a display number: it must be 1 or more", display)
	}

	p := Plan{Platform: "darwin"}
	switch mon := monitor(); mon {
	case m1ddc:
		// `m1ddc display <n> set input <vcp>`, where the value is the same
		// MCCS number that goes in the settings file.
		p.Steps = append(p.Steps, Step{
			Bin:  mon,
			Args: []string{"display", strconv.Itoa(display), "set", "input", decVCP(vcp)},
		})
	case ddcctl:
		// `ddcctl -d <n> -i <vcp>`, which is kfix/ddcctl's own documented form.
		p.Steps = append(p.Steps, Step{
			Bin:  mon,
			Args: []string{"-d", strconv.Itoa(display), "-i", decVCP(vcp)},
		})
	default:
		// Named here rather than at the point of failure, so the error says
		// what to install instead of what was not found.
		return Plan{}, fmt.Errorf("neither m1ddc nor ddcctl is installed, " +
			"so there is nothing to move the display with. " +
			"On Apple Silicon: `brew install m1ddc`")
	}

	if channel == "0" {
		return p, nil
	}
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
// ProbeLimit is how many display numbers the ddcctl probe will try. Only used
// on Intel Macs, where ddcctl is the fallback and has no way to list displays.
const ProbeLimit = 4

// Detect lists the displays this Mac can switch.
//
// With m1ddc it is a listing: `m1ddc display list` prints one line per external
// display, numbered from 1, and skips the built-in one. That is the numbering
// the switch uses, so it is the numbering to show.
//
// With ddcctl there is no listing mode at all, so the numbers are found by
// asking each one in turn whether it answers. See probeDDCCTL.
func (d Darwin) Detect(ctx context.Context) ([]Display, error) {
	if monitor() == m1ddc {
		return listWithM1DDC(ctx)
	}
	return probeDDCCTL(ctx)
}

// listWithM1DDC reads `m1ddc display list`, whose lines look like:
//
//	[1] DELL S2725DC (0F0E1D2C-...)
//
// The number is what `m1ddc display <n>` takes. Nothing here proves DDC works
// on a display: a luminance read is not a good proxy, because a monitor can
// refuse that and still switch inputs. A display that cannot be switched says
// so when it is switched, which is the report that matters.
func listWithM1DDC(ctx context.Context) ([]Display, error) {
	text, err := runStepRaw(ctx, Step{Bin: m1ddc, Args: []string{"display", "list"}}, m1ddcTimeout)
	if err != nil {
		return nil, fmt.Errorf("m1ddc display list: %w", err)
	}
	var out []Display
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		close := strings.Index(line, "]")
		if close < 0 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(line[1:close]))
		if err != nil || n < 1 {
			continue
		}
		name := strings.TrimSpace(line[close+1:])
		// The trailing parenthesised UUID is not something to show a person.
		if i := strings.LastIndex(name, " ("); i > 0 {
			name = strings.TrimSpace(name[:i])
		}
		if name == "" {
			name = "display " + strconv.Itoa(n)
		}
		out = append(out, Display{Number: n, Name: name, Usable: true})
	}
	if len(out) == 0 {
		// m1ddc says "No external display found, aborting" and exits non-zero,
		// so reaching here means it printed something unreadable rather than
		// nothing. Refusing is the safe direction: an empty list would read as
		// a machine with no monitors.
		return nil, fmt.Errorf("m1ddc did not name any displays in %q", strings.TrimSpace(text))
	}
	return out, nil
}

// probeDDCCTL finds display numbers by asking each one.
//
// ddcctl has no listing mode: its arguments are the settings to change, and -d
// only picks which display to change them on. A number past the end of its list
// is refused at once, and one that exists either answers a read or does not.
//
// Three outcomes are told apart, because they need different fixes:
//
//	answers     the number works and the monitor speaks DDC
//	refused     no such number, so a lower one is right
//	no answer   the number may be right but the monitor is not replying
func probeDDCCTL(ctx context.Context) ([]Display, error) {
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
		case err != nil && strings.Contains(text+err.Error(), "Usage"):
			d.Why = "ddcctl has no display with this number"
		default:
			d.Why = "the monitor did not answer"
		}
		out = append(out, d)
	}
	return out, nil
}
