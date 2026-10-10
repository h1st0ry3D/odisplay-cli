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

	// Step 0 is always the monitor. Nothing after it runs unless it succeeded.
	if err := b.Run(ctx, plan.Steps[0]); err != nil {
		if errors.Is(err, backend.ErrMissing) {
			// Named from the step that failed rather than written down here.
			// This said "ddcutil" on every platform, which on a Mac sent
			// someone looking for a Linux program they had never heard of
			// while the one they had installed was the one being missed.
			return Result{
				Code: CodeMissing,
				Message: fmt.Sprintf("%s is not installed, so the display cannot be moved.",
					filepath.Base(plan.Steps[0].Bin)),
				Detail: err.Error(),
			}
		}
		return Result{
			Code:    CodeDisplayFailed,
			Message: fmt.Sprintf("Could not switch to %s. The devices were left where they were.", label),
			Detail:  err.Error(),
		}
	}

	for _, step := range plan.Steps[1:] {
		if err := b.Run(ctx, step); err != nil {
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
			return Result{
				Code:    CodeDevicesFailed,
				Message: fmt.Sprintf("Sent %s, but the devices did not move. The switch underneath your mouse or keyboard is the way back.", label),
				Detail:  err.Error(),
			}
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
