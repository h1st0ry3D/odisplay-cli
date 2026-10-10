//go:build darwin

package backend

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	ddcctl   = "/usr/local/bin/ddcctl"
	logigate = "/usr/local/bin/logi-gate"

	// logi-gate probes HID devices before it switches, so it needs more room
	// than a plain DDC call.
	logigateTimeout = 15 * time.Second
)

// Darwin is the macOS backend: ddcctl for the monitor, logi-gate for the
// devices.
//
// ddcctl's input source values are the same MCCS numbers ddcutil uses, written
// in decimal: DisplayPort-1 is 15, HDMI-1 is 17, USB-C is 27. One number is
// stored in the config and each backend writes it the way its program wants.
//
// logi-gate moves every paired device in one call and takes no device names,
// which is why the device list is unused here. Unlike Solaar on Linux it needs
// its HID engine installed under a passwordless sudoers rule, so this path
// does need root at install time.
type Darwin struct{}

func (Darwin) Platform() string { return "darwin" }

// Plan builds the steps. The monitor is always first.
func (d Darwin) Plan(display, vcp int, channel string) (Plan, error) {
	if err := validVCP(vcp); err != nil {
		return Plan{}, err
	}
	p := Plan{Platform: "darwin"}
	// -d is a one-based display number, -i the MCCS value in decimal.
	p.Steps = append(p.Steps, Step{
		Bin:  ddcctl,
		Args: []string{"-d", strconv.Itoa(display), "-i", decVCP(vcp)},
	})
	if channel == "0" {
		return p, nil
	}
	p.Steps = append(p.Steps, Step{Bin: logigate, Args: []string{channel}})
	return p, nil
}

// Run executes one step. logi-gate gets the larger budget because it probes
// HID devices before it switches.
func (d Darwin) Run(ctx context.Context, s Step) error {
	timeout := ddcTimeout
	if s.Bin == logigate {
		timeout = logigateTimeout
	}
	_, err := runStep(ctx, s, timeout)
	return err
}

// New returns the macOS backend. The device list is unused here because
// logi-gate moves every paired device in one call, but it is accepted so the
// call site is the same on both platforms.
func New(_ []string) Backend { return Darwin{} }

// Detect lists what ddcctl can see. It reads the monitor's identity and
// changes nothing.
//
// ddcctl prints one line per display with its own number at the front, so the
// number is read rather than assumed. Its numbering is also positional: a
// display that stops being seen shifts the ones after it.
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
			Usable: true,
		})
	}
	return out, nil
}
