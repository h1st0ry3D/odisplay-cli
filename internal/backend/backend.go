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
	"sync"
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
	// Number is the index the program itself uses, read from its own output.
	// Zero when the program gives it none, which is what it does to a display
	// it will not accept. A zero is not addressable however the config is set.
	//
	// Not stable forever: a display that stops answering drops out of the list
	// and the others are renumbered.
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

// Addressable returns the displays a --display number can actually reach, in
// the order the program numbers them.
//
// A connector the program rejected has no number at all, so it cannot be
// addressed however the config is set. Counting it would name a display that
// does not exist.
func Addressable(displays []Display) []Display {
	var out []Display
	for _, d := range displays {
		if d.Usable && d.Number > 0 {
			out = append(out, d)
		}
	}
	return out
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

// waitDelay is how long Wait keeps draining a child's output after the context
// has already expired and the child has been killed. Short, because by then the
// answer is decided and the only thing left is a pipe some grandchild is still
// holding open.
const waitDelay = 2 * time.Second

// probeTimeout is how long a single "is this display there" read gets. Short,
// because it runs once per number and the answer is only ever advisory: a
// monitor that is slow to answer is not wrong, and doctor says so rather than
// waiting on it.
const probeTimeout = 3 * time.Second

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
	// A child that leaves a grandchild holding the pipe keeps Wait blocked even
	// after the child itself has been killed, because Wait also waits for the
	// goroutines draining stdout and stderr. Those goroutines then wait for a
	// writer nobody is going to kill. WaitDelay bounds that, so a timeout is
	// actually a timeout: without it, `sh -c 'sleep 300'` holds the tool for
	// the full 300 seconds however short the budget was.
	cmd.WaitDelay = waitDelay
	// One capture for both streams, not two writers over one buffer: os/exec
	// gives stdout and stderr a goroutine each whenever they are not the same
	// writer value, so a shared *bytes.Buffer is written from two threads at
	// once. That buffer holds the message explaining what happened to the
	// user's keyboard, so it is not a place to leave a race.
	out := &capture{left: outputLimit}
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	text := out.String()
	if err != nil {
		// This has to come before the ExitError unwrap. CommandContext kills
		// the child when the context expires, and Wait then reports an
		// *exec.ExitError with code -1. Checked in the other order, every
		// timeout reads "exited with -1", which says nothing about a child
		// that never came back.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return text, fmt.Errorf("%s did not finish within %s, so it was stopped",
				filepath.Base(s.Bin), timeout)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if strings.TrimSpace(text) == "" {
				text = fmt.Sprintf("exited with %d", exit.ExitCode())
			}
			return text, errors.New(flatten(text))
		}
		return text, fmt.Errorf("%s: %w", filepath.Base(s.Bin), err)
	}
	return text, nil
}

// Installed reports whether a program is on this machine and can be run, for
// the doctor check. It does not run it.
//
// This uses LookPath rather than Stat, so it agrees with what runStep will
// actually find. Stat would call a non-executable file installed, and doctor
// would report ready for a switch that then exits "not installed".
func Installed(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// capture collects a child's output from both streams under one budget.
//
// os/exec writes stdout and stderr from a goroutine each unless they are the
// same writer value, so this has to be safe to write from two threads at once.
// One buffer and one remaining count, so the cap is the cap rather than twice
// the cap.
type capture struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	left int
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left <= 0 {
		// Report the full length so the child does not see a short write and
		// treat it as a broken pipe.
		return len(p), nil
	}
	n := len(p)
	if n > c.left {
		n = c.left
	}
	c.buf.Write(p[:n])
	c.left -= n
	return len(p), nil
}

func (c *capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// flatten makes a child's output into one line. A wrapped message reads as one
// run-together word otherwise, and the panel has one line to show it in.
func flatten(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// validChannel rejects a value that is not one of the Easy-Switch channels.
//
// Off is handled by the caller before this, because off means no device step at
// all rather than a channel to send. Both backends use this so they cannot
// disagree about which channels exist.
//
// An empty channel reaches here when a settings file has no "host" for an
// input. That used to become `change-host ”` on Linux: a command that runs,
// fails, and has already moved the monitor by the time it does. Caught here, it
// stops before anything runs, which is the direction that costs nobody their
// keyboard.
func validChannel(channel string) error {
	n, err := strconv.Atoi(channel)
	if err != nil || n < 1 || n > 3 {
		return fmt.Errorf("channel %q is not one this machine accepts: "+
			"1, 2 or 3, or 0 to leave the keyboard and mouse alone", channel)
	}
	return nil
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
