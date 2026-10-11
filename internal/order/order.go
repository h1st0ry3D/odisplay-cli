// Package order holds the order of operations, which is the one thing about
// this tool that can leave someone unable to type.
//
// The rule: the monitor moves first, and the keyboard and mouse move only once
// it has. A failed monitor switch is the one situation where the devices stay
// put, because a monitor that did not move means the user is still looking at
// the machine whose keyboard they have. Moving them anyway would leave them
// staring at one machine with the input attached to another, with no way back
// from the panel because the pointer went with it.
//
// The steps themselves come from the backend, which builds them but never
// orders or runs them. That split is what lets --dry-run print exactly what
// would run, and lets a test check the argument arrays without touching any
// hardware.
package order

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/h1st0ry3D/odisplay-cli/internal/backend"
	"github.com/h1st0ry3D/odisplay-cli/internal/config"
)

// The exit codes are the contract with whatever is showing the panel. In
// particular CodeDisplayFailed and CodeDevicesFailed differ because they mean
// opposite things about where the user's keyboard is.
const (
	CodeOK            = 0
	CodeUsage         = 1
	CodeDisplayFailed = 2
	CodeDevicesFailed = 3
	CodeMissing       = 4
)

// Result says what happened, in words meant for one line of a panel.
type Result struct {
	Code    int
	Message string
	Detail  string
}

// Switch moves the monitor to the named input and then, if that input has a
// channel set, the devices with it.
//
// With dryRun set nothing is executed: the plan is built and reported, which is
// the only safe way to look at this tool before pointing it at real hardware.
func Switch(ctx context.Context, b backend.Backend, cfg config.Config, key string, dryRun bool) Result {
	in, ok := cfg.Input(key)
	if !ok {
		return Result{Code: CodeUsage, Message: fmt.Sprintf("No input called %q.", key)}
	}
	label := in.Name
	if label == "" {
		label = in.Key
	}

	plan, err := b.Plan(cfg.Display, in.VCP, string(in.Host))
	if err != nil {
		return Result{Code: CodeUsage, Message: err.Error()}
	}
	if len(plan.Steps) == 0 {
		return Result{Code: CodeUsage, Message: "Nothing to run for this input."}
	}

	if dryRun {
		return Result{
			Code:    CodeOK,
			Message: fmt.Sprintf("%s, to %s, would run:\n%s", label, b.Platform(), plan.Text()),
		}
	}

	// The first step is always the monitor. Nothing after it runs unless it
	// succeeded, which is the one thing this program is for.
	monitor := plan.Steps[0]
	if err := b.Run(ctx, monitor); err != nil {
		if errors.Is(err, backend.ErrMissing) {
			// Named from the step that failed rather than written down here.
			// This said "ddcutil" on every platform, which on a Mac sent
			// someone looking for a Linux program they had never heard of
			// while the one they had installed was the one being missed.
			return Result{
				Code: CodeMissing,
				Message: fmt.Sprintf("%s is not installed, so the display cannot be moved.",
					filepath.Base(monitor.Bin)),
				Detail: err.Error(),
			}
		}
		return Result{
			Code:    CodeDisplayFailed,
			Message: fmt.Sprintf("Could not switch to %s. The devices were left where they were.", label),
			Detail:  err.Error(),
		}
	}

	// The devices, in the order the plan gives them. moved is how many did
	// something, which is what decides how a step with nothing left to do is
	// read: with something already sent it is the end, and with nothing sent it
	// is the whole of what happened.
	moved := 0
	for _, step := range plan.Steps[1:] {
		err := b.Run(ctx, step)

		if err == nil {
			moved++
			continue
		}
		if errors.Is(err, backend.ErrNothingToDo) && moved > 0 {
			// Everything that was here has been sent. One platform moves one
			// device per call and says this once there are none left, so after
			// at least one success it is the end of the sequence. Reporting it
			// as a failure would say the keyboard stayed when it had left.
			break
		}
		if errors.Is(err, backend.ErrMissing) {
			// The monitor moved and the devices did not, so the user is now
			// looking at the other machine without input.
			return Result{
				Code: CodeMissing,
				Message: fmt.Sprintf("Sent %s, but %s is not installed, so your keyboard and mouse stayed here.",
					label, filepath.Base(step.Bin)),
				Detail: err.Error(),
			}
		}
		// What to say depends on whether anything left, which is the point of
		// counting. With nothing moved the keyboard and mouse are still here
		// and there is nothing to recover from, so pointing at the button
		// underneath would send someone to press it for no reason.
		//
		// Both are well inside the eighty characters a panel shows, because a
		// message that is cut off is one whose last words nobody reads.
		if moved == 0 {
			return Result{
				Code:    CodeDevicesFailed,
				Message: fmt.Sprintf("Sent %s, but the keyboard and mouse stayed here.", label),
				Detail:  err.Error(),
			}
		}
		return Result{
			Code:    CodeDevicesFailed,
			Message: fmt.Sprintf("Sent %s. A device did not follow; the button underneath is the way back.", label),
			Detail:  err.Error(),
		}
	}

	if string(in.Host) == config.HostOff {
		return Result{Code: CodeOK, Message: "Sent " + label + "."}
	}
	return Result{
		Code:    CodeOK,
		Message: fmt.Sprintf("Sent %s, devices on host %s.", label, in.Host),
	}
}
