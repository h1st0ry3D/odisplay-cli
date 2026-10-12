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

// monitorProgram is the program that moves the display, and it is a choice
// rather than a constant because the two are for different Macs.
//
// m1ddc is preferred. It is the one that works on Apple Silicon, which is every
// Mac made since 2020: ddcctl has an open issue from the M1 launch asking for
// this, its last commit is from November 2022, and the reason is that Apple
// replaced the framebuffer interfaces it drives. Its DDC calls do nothing on
// those machines while still reporting that they ran.
//
// ddcctl is kept as the fallback because it is the one that works on Intel
// Macs, where m1ddc explicitly does not.
func monitorProgram() string {
	if Installed(m1ddc) {
		return m1ddc
	}
	if Installed(ddcctl) {
		return ddcctl
	}
	return ""
}

// searchDirs is where the programs are looked for, in order.
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

// sudo is how a privilege is asked for, and -n is the flag that makes it give
// up rather than prompt. A menu bar item has no terminal to be prompted on, so
// waiting there would hang with nothing on screen to explain it.
const sudo = "/usr/bin/sudo"

// systemDirs is where macOS keeps the commands it ships to sleep and to shut
// down, looked in that order.
//
// Both the bin and the sbin directories are here because pmset is an ordinary
// user command and shutdown is an administrative one, and writing the paths out
// from memory put pmset in the wrong one. There is no way to check that from a
// Linux machine, and the only place it shows up is on a Mac that is being left,
// which is the worst moment to find out.
//
// This is not searchDirs: that is where programs someone installed are looked
// for, and this is where the ones the system provides are.
func systemDirs() []string {
	return []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin"}
}

var (
	// pmset sleeps the machine without privileges.
	pmsetBin = mustFind("pmset", systemDirs()...)
	// shutdown is the one a sudoers rule has to name by its full path, so it is
	// resolved once, here, and reported where that is written down.
	shutdownBin = mustFind("shutdown", systemDirs()...)
)

// Power is the step that sleeps or powers off this Mac.
//
// Suspending needs no privileges. Shutting down does, and there is no way to
// ask for one from a menu bar item, so it is attempted through sudo and a
// refusal is reported. That is the honest outcome: better a line saying the
// machine will not power off than one implying it will.
//
// The rule that makes it work is one line in sudoers, naming shutdown by the
// full path sudo sees. Sudoers matches the path, not the name, so this has to
// be the same string that ends up in the rule:
//
//	yourname ALL=(root) NOPASSWD: <shutdownBin> -h now
func (Darwin) Power(action string) []Step {
	switch action {
	case "sleep":
		return []Step{{Bin: pmsetBin, Args: []string{"sleepnow"}}}
	case "shutdown":
		return []Step{{Bin: sudo, Args: []string{"-n", shutdownBin, "-h", "now"}}}
	}
	return nil
}

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
// itself and moves one per call, and there is no flag to choose. So the device
// count has to come from somewhere, and the config's device list is what says
// it. The names in that list are Solaar's and are not used here; only how many
// there are.
//
// Which device it picks is not a preference that can be argued with: it ranks a
// mouse below a keyboard and stops at the first one that answers, so a mouse is
// always chosen when there is one. That makes this loop a retry rather than a
// list of devices to move.
//
// It works when the first call sends the mouse somewhere it is no longer
// enumerating, which makes the keyboard the next candidate. It does not work
// when the mouse stays visible -- a channel that is this same machine, for
// instance -- because the second call finds the mouse again and does nothing
// more. There is no way from here to tell which happened, so the steps are
// built either way and the second is harmless when it is a repeat.
type Darwin struct {
	// Count is how many Logitech devices to move. Names are ignored.
	Count int
}

func (Darwin) Platform() string { return "darwin" }

// Monitor names whichever of the two this Mac has, and falls back to the one
// that would have to be installed, so a label always names something real.
func (Darwin) Monitor() string {
	if m := monitorProgram(); m != "" {
		return filepath.Base(m)
	}
	return "m1ddc"
}

// Plan builds the steps. The monitor is always first.
func (d Darwin) Plan(display, vcp int, channel string) (Plan, error) {
	if err := validVCP(vcp); err != nil {
		return Plan{}, err
	}
	if display < 1 {
		return Plan{}, fmt.Errorf("display %d is not a display number: it must be 1 or more", display)
	}

	p := Plan{Platform: "darwin"}
	switch mon := monitorProgram(); mon {
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
	for range d.Count {
		p.Steps = append(p.Steps, Step{Bin: mxswitch, Args: []string{channel}})
	}
	return p, nil
}

// Run executes one step. mxswitch gets the larger budget because it enumerates
// HID devices before it sends anything.
func (Darwin) Run(ctx context.Context, s Step) error {
	timeout := ddcTimeout
	if s.Bin == mxswitch {
		timeout = mxswitchTimeout
	}
	text, err := runStep(ctx, s, timeout)
	if err != nil && s.Bin == mxswitch && isNoDevice(text) {
		// mxswitch says this when it cannot find a device supporting
		// ChangeHost. There are two reasons and it cannot tell them apart: the
		// devices have all been moved already, or none can be reached. The
		// caller knows which, because it knows whether anything has moved yet.
		return fmt.Errorf("%s: %w", text, ErrNothingToDo)
	}
	return err
}

// isNoDevice is mxswitch's own wording for finding nothing to move.
func isNoDevice(text string) bool {
	return strings.Contains(text, "No Logitech device supporting ChangeHost found")
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
	return Darwin{Count: count}
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
func (Darwin) Detect(ctx context.Context) ([]Display, error) {
	if monitorProgram() == m1ddc {
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
