// Package backend builds and runs the two things this tool does, on the
// platform it is running on.
//
// Building and running are separate on purpose. A Plan is the list of commands
// a switch would run, which is what --dry-run prints and what the tests check.
// Nothing here decides the order the steps run in: that belongs to one place,
// the order package, so it cannot differ between platforms.
package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrMissing reports that a required program is not installed. It is its own
// error so the CLI can exit with its own code, because "Solaar is not
// installed" is a different situation from "Solaar said no".
var ErrMissing = errors.New("backend program not installed")

// Step is one program invocation. Every element of Args is its own argument.
// Nothing goes through a shell, so a device name with spaces in it is one
// argument and a device name with a quote in it cannot become a second
// command.
type Step struct {
	Bin  string
	Args []string
}

// String renders the step the way a person would type it. It is for reading and
// for pasting, not for executing: the tool always runs the argv array above.
func (s Step) String() string {
	parts := make([]string, 0, len(s.Args)+1)
	parts = append(parts, s.Bin)
	for _, a := range s.Args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote quotes one argument if it needs it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`*?[]{}();&|<>#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Plan is the ordered list of steps for one switch. The first is always the
// monitor; the rest are the devices.
type Plan struct {
	Platform string
	Steps    []Step
}

// Text renders a plan as one command per line, for --dry-run.
func (p Plan) Text() string {
	lines := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		lines = append(lines, s.String())
	}
	return strings.Join(lines, "\n")
}

// Programs lists the distinct programs a plan will run, for the doctor check.
func (p Plan) Programs() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range p.Steps {
		if !seen[s.Bin] {
			seen[s.Bin] = true
			out = append(out, s.Bin)
		}
	}
	return out
}

// Display is one monitor the machine can see.
type Display struct {
	// Number is the index the program itself uses. It is positional, not
	// stable: unplugging a cable or a laptop panel going to sleep can shift
	// it, which is why doctor reports what it sees rather than trusting the
	// config.
	Number int
	// Name is the connector or model, for a person to recognise.
	Name string
	// Usable is false when a switch sent to this display would do nothing.
	Usable bool
	// Why is the reason it is not usable, in the program's own words. A
	// laptop panel that cannot do DDC/CI is a permanent answer, while a
	// monitor that is merely not answering right now is a different
	// problem with a different fix, so the two are not reported alike.
	Why string
}

// Backend plans and runs the switch for one platform.
type Backend interface {
	Platform() string
	// Plan builds the steps. It does not run anything.
	Plan(display, vcp int, channel string) (Plan, error)
	// Run executes one step.
	Run(ctx context.Context, s Step) error
	// Detect lists the monitors this machine can see. It reads the
	// hardware's identity and capabilities and changes nothing.
	Detect(ctx context.Context) ([]Display, error)
}

// path is the only PATH a child ever sees. The programs are named absolutely
// anyway, so this is belt and braces.
const searchPath = "/usr/bin:/bin"

// env is the environment every child runs in: a fixed PATH and nothing else, so
// no user setting can change what a name resolves to. HOME is kept because
// ddcutil and Solaar need somewhere to put their state, and a fixed
// XDG_CACHE_HOME keeps ddcutil from printing a cache warning on every run.
func env() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return []string{
		"PATH=" + searchPath,
		"LANG=C",
		"LC_ALL=C",
		"HOME=" + home,
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache", "odisplay"),
	}
}

// outputLimit caps what a child can make this tool hold in memory. ddcutil
// prints a few lines or nothing, so this is a backstop rather than a budget.
const outputLimit = 64 << 10

// Timeouts are the backstop for a call that does not come back at all. ddcutil
// can hang when an I2C adapter is taken away underneath it. Solaar is a Python
// program that opens the receiver, and a device that is asleep, or already on
// another host, leaves it waiting rather than failing.
const (
	ddcTimeout    = 8 * time.Second
	solaarTimeout = 10 * time.Second
)

// Run executes one step and returns its output as a single line, because the
// panel has one line to show an error in.
func runStep(ctx context.Context, s Step, timeout time.Duration) (string, error) {
	text, err := runStepRaw(ctx, s, timeout)
	return flatten(text), err
}

// runStepRaw is runStep without the flattening, for the one caller that parses
// multi-line output.
func runStepRaw(ctx context.Context, s Step, timeout time.Duration) (string, error) {
	if _, err := exec.LookPath(s.Bin); err != nil {
		return "", fmt.Errorf("%s: %w", s.Bin, ErrMissing)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.Bin, s.Args...)
	cmd.Env = env()
	var buf bytes.Buffer
	cmd.Stdout = &limited{w: &buf, left: outputLimit}
	cmd.Stderr = &limited{w: &buf, left: outputLimit}

	err := cmd.Run()
	text := buf.String()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if strings.TrimSpace(text) == "" {
				text = fmt.Sprintf("exited with %d", exit.ExitCode())
			}
			return text, errors.New(flatten(text))
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return text, fmt.Errorf("%s did not finish within %s", filepath.Base(s.Bin), timeout)
		}
		return text, fmt.Errorf("%s: %w", filepath.Base(s.Bin), err)
	}
	return text, nil
}

// Installed reports whether a program is on this machine, for the doctor
// check. It does not run it.
func Installed(bin string) bool {
	_, err := os.Stat(bin)
	return err == nil
}

// limited is a writer that stops after n bytes, so a runaway child cannot grow
// this tool's memory without limit.
type limited struct {
	w    *bytes.Buffer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.left <= 0 {
		return len(p), nil
	}
	n := len(p)
	if n > l.left {
		n = l.left
	}
	l.w.Write(p[:n])
	l.left -= n
	return len(p), nil
}

// flatten makes a child's output into one line. A wrapped message reads as one
// run-together word otherwise, and the panel has one line to show it in.
func flatten(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// validVCP rejects a value that is not an MCCS input source number before it
// can become an argument.
func validVCP(vcp int) error {
	if vcp < 0 || vcp > 0xff {
		return fmt.Errorf("input source %d is out of range, 0 to 255", vcp)
	}
	return nil
}

// hexVCP is how ddcutil wants the value, decVCP how ddcctl wants it. They are
// the same number.
//
// Zero padded to two digits so DisplayPort-1 reads 0x0f rather than 0xf, which
// is how a monitor documents it and what a capability dump prints.
func hexVCP(vcp int) string { return fmt.Sprintf("0x%02x", vcp) }

func decVCP(vcp int) string { return strconv.Itoa(vcp) }
