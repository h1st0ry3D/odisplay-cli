//go:build linux

package backend

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	ddcutil = "/usr/bin/ddcutil"
	solaar  = "/usr/bin/solaar"
	// systemctl is how a desktop session suspends or powers off. Both go
	// through polkit, which allows them for the user at the console, so
	// neither needs sudo here. The configure step establishes which.
	systemctl = "/usr/bin/systemctl"
)

// Power is the step that suspends or powers off this machine.
//
// Both are the session's own, not a privileged shutdown: systemctl asks polkit,
// and a user sitting at the machine is allowed. One that is not, a remote
// session for instance, gets a refusal which is reported rather than retried
// with more force.
func (Linux) Power(action string) []Step {
	switch action {
	case "sleep":
		return []Step{{Bin: systemctl, Args: []string{"suspend"}}}
	case "shutdown":
		return []Step{{Bin: systemctl, Args: []string{"poweroff"}}}
	}
	return nil
}

// Linux is the Linux backend: ddcutil for the monitor, Solaar for the devices.
//
// Solaar needs one call per device, because it addresses a device by the name
// it prints and a name that does not match exactly is an error rather than a
// silent no-op.
//
// The order they are listed in is the order they are sent, and the list is the
// config's, so it is the person's to choose. It is not sorted here or guessed
// at, because the names are free text: nothing can tell a mouse from a keyboard
// except the person who typed them.
type Linux struct {
	// Names are the device names Solaar matches on, in the order they are sent.
	Names []string
}

func (Linux) Platform() string { return "linux" }

// Monitor is ddcutil, which is what moves the display on Linux.
func (Linux) Monitor() string { return "ddcutil" }

// Plan builds the steps. The monitor is always first.
func (l Linux) Plan(display, vcp int, channel string) (Plan, error) {
	if err := validVCP(vcp); err != nil {
		return Plan{}, err
	}
	p := Plan{Platform: "linux"}
	// ddcutil wants the value in hex and the display number from
	// `ddcutil detect`, which is its own index.
	p.Steps = append(p.Steps, Step{
		Bin:  ddcutil,
		Args: []string{"setvcp", "60", hexVCP(vcp), "--display", strconv.Itoa(display)},
	})
	if channel == "0" {
		return p, nil
	}
	if err := validChannel(channel); err != nil {
		return Plan{}, err
	}
	for _, device := range l.Names {
		if device == "" {
			continue
		}
		p.Steps = append(p.Steps, Step{
			Bin:  solaar,
			Args: []string{"config", device, "change-host", channel},
		})
	}
	return p, nil
}

// Run executes one step. Solaar gets the larger budget because it is a Python
// program that opens the receiver and waits on a device that may not answer.
func (Linux) Run(ctx context.Context, s Step) error {
	timeout := ddcTimeout
	if s.Bin == solaar {
		timeout = solaarTimeout
	}
	_, err := runStep(ctx, s, timeout)
	return err
}

// New returns the Linux backend. Each platform defines its own, rather than one
// New switching on runtime.GOOS, so no other platform's type has to exist for
// this to compile and a build for an unsupported platform gets the refusing
// backend instead of a half-working one.
func New(devices []string) Backend { return Linux{Names: devices} }

// Detect lists what ddcutil can see. It reads EDID and capabilities and changes
// nothing on the hardware.
//
// The numbers come from ddcutil, not from counting its output. ddcutil numbers
// the displays it will accept and gives a rejected connector no number at all,
// so the same monitor is "Display 1" whether or not a laptop panel is also
// plugged in. The numbers are not stable forever: a display that stops
// answering over I2C drops out and the rest are renumbered.
func (Linux) Detect(ctx context.Context) ([]Display, error) {
	text, err := runStepRaw(ctx, Step{Bin: ddcutil, Args: []string{"detect"}}, ddcTimeout)
	if err != nil {
		return nil, fmt.Errorf("ddcutil detect: %w", err)
	}
	return parseDisplays(text), nil
}

// parseDisplays turns ddcutil detect output into the list doctor reports.
//
// Each display is a block separated by a blank line, with its fields indented
// under it. The first line is the verdict, and it is the only thing that says
// which number ddcutil will accept:
//
//	Display 1          addressable, number 1
//	Invalid display    not addressable, and NOT given a number
//
// It is a separate function so it can be checked against captured output without
// running anything against the hardware.
func parseDisplays(text string) []Display {
	var out []Display
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		d := Display{Usable: true}
		var reason string
		for _, line := range lines {
			field, value, ok := strings.Cut(line, ":")
			if ok {
				switch strings.TrimSpace(field) {
				case "DRM_connector":
					d.Name = strings.TrimSpace(value)
				case "Model":
					// EDID repeats the model once per descriptor. The first is
					// the monitor's own model line.
					if d.Name == "" {
						d.Name = strings.TrimSpace(value)
					}
				}
				continue
			}
			// ddcutil puts its explanation last, as a whole sentence. Keep it,
			// because "this panel does not do DDC/CI" and "this monitor is not
			// answering" are different problems with different fixes.
			if trimmed := strings.TrimSpace(line); trimmed != "" &&
				!strings.HasSuffix(trimmed, ":") {
				reason = trimmed
			}
		}

		verdict := strings.TrimSpace(lines[0])
		switch {
		case strings.HasPrefix(verdict, "Display "):
			// ddcutil numbers the displays it will accept, starting at 1.
			// Read its number rather than counting: a connector it rejected
			// takes no number at all, so counting would shift every display
			// after it up by one and name a display that does not exist.
			number, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(verdict, "Display ")))
			if err != nil {
				d.Number = 0
				d.Usable = false
				d.Why = "ddcutil printed an unreadable display number: " + verdict
				break
			}
			d.Number = number
		default:
			// "Invalid display", "Unsupported display". This one has no
			// number, so --display cannot reach it.
			d.Number = 0
			d.Usable = false
			d.Why = reason
		}

		if d.Name == "" {
			d.Name = "(unnamed)"
		}
		out = append(out, d)
	}
	return out
}
