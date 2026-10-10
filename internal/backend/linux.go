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
)

// Linux is the Linux backend: ddcutil for the monitor, Solaar for the devices.
//
// Solaar needs one call per device, because it addresses a device by the name
// it prints and a name that does not match exactly is an error rather than a
// silent no-op. The order they are listed in is the order they are sent, and the
// mouse goes last so the pointer is still here if the keyboard move is what
// went wrong.
type Linux struct {
	Devices []string
}

func (Linux) Platform() string { return "linux" }

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
	for _, device := range l.Devices {
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
func (l Linux) Run(ctx context.Context, s Step) error {
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
func New(devices []string) Backend { return Linux{Devices: devices} }

// Detect lists what ddcutil can see. It reads EDID and capabilities and changes
// nothing on the hardware.
//
// ddcutil does not print the display numbers it uses; the number is the
// position in its listing. So the list is numbered here, in order, which is the
// only way to tell the user what to put in the config. The number is not stable
// across reboots or cable changes, because a display that stops enumerating
// shifts everything after it up by one.
func (l Linux) Detect(ctx context.Context) ([]Display, error) {
	text, err := runStepRaw(ctx, Step{Bin: ddcutil, Args: []string{"detect"}}, ddcTimeout)
	if err != nil {
		return nil, fmt.Errorf("ddcutil detect: %w", err)
	}
	return parseDisplays(text), nil
}

// parseDisplays turns ddcutil detect output into the list doctor reports.
//
// Each display is a block separated by a blank line, with its fields indented
// under it. The first line is the verdict, either "Display N" or "Invalid
// display". It is a separate function so it can be checked against captured
// output without running anything against the hardware.
func parseDisplays(text string) []Display {
	var out []Display
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		d := Display{Number: len(out) + 1, Usable: true}
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
				!strings.HasPrefix(trimmed, "Invalid display") &&
				!strings.HasSuffix(trimmed, ":") {
				reason = trimmed
			}
		}
		if strings.HasPrefix(lines[0], "Invalid") || strings.HasPrefix(lines[0], "Unsupported") {
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
