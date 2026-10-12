package order

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/h1st0ry3D/odisplay-cli/internal/backend"
	"github.com/h1st0ry3D/odisplay-cli/internal/config"
)

// fake plans like a real backend and records what it was asked to run. It is
// where the argv arrays can be checked without any hardware.
type fake struct {
	plan    backend.Plan
	power   []backend.Step
	planErr error
	runErr  map[int]error // keyed by position in the plan's move steps
	// powerErr is what the action step returns, kept apart from runErr because
	// the action is not a move and has no position among them.
	powerErr error
	// ran is the move steps that ran, by position. after is the action steps,
	// as command lines, so a test can see whether it ran and in what order.
	ran   []int
	after []string
}

func (f *fake) Platform() string { return "fake" }

func (f *fake) Monitor() string { return "ddcutil" }

// Grant is empty: the ordering does not print rules, and nothing in these tests
// needs a password.
func (f *fake) Grant(string) string { return "" }

// power is what the fake says the platform would run afterwards, so a test can
// see whether the ordering ran it.
func (f *fake) Power(string) []backend.Step { return f.power }

func (f *fake) Devices(context.Context) (backend.Connected, error) {
	return backend.Connected{}, nil
}

func (f *fake) Plan(display, vcp int, channel string) (backend.Plan, error) {
	if f.planErr != nil {
		return backend.Plan{}, f.planErr
	}
	if f.plan.Platform == "" {
		f.plan.Platform = "fake"
		f.plan.Steps = []backend.Step{
			{Bin: "/usr/bin/ddcutil", Args: []string{"setvcp", "60", "0x0f", "--display", "1"}},
		}
		if channel != "0" {
			for _, d := range []string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"} {
				f.plan.Steps = append(f.plan.Steps, backend.Step{
					Bin: "/usr/bin/solaar", Args: []string{"config", d, "change-host", channel},
				})
			}
		}
	}
	return f.plan, nil
}

func (f *fake) Run(_ context.Context, s backend.Step) error {
	for i, step := range f.plan.Steps {
		if reflect.DeepEqual(step, s) {
			f.ran = append(f.ran, i)
			return f.runErr[i]
		}
	}
	f.after = append(f.after, s.String())
	return f.powerErr
}

// Detect is only used by the doctor check, which is not what order decides
// anything from, so the fake has nothing to report.
func (f *fake) Detect(context.Context) ([]backend.Display, error) {
	return nil, errors.New("not implemented")
}

func cfgWith(host config.Host) config.Config {
	c := config.Default()
	c.Display = 1
	c.Inputs = []config.Input{
		{Key: "usbc", Name: "X", VCP: 27, Host: "0"},
		{Key: "dp", Name: "Mac Mini", VCP: 15, Host: host},
	}
	return c
}

// cfgWithPower is one input that asks this machine to do something with itself
// afterwards.
func cfgWithPower(host config.Host, power config.Power) config.Config {
	c := cfgWith(host)
	c.Inputs[1].Power = power
	return c
}

func TestMonitorMovesBeforeDevices(t *testing.T) {
	f := &fake{}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", false)
	if res.Code != CodeOK {
		t.Fatalf("code = %d (%s: %s)", res.Code, res.Message, res.Detail)
	}
	if len(f.ran) != 3 {
		t.Fatalf("ran %v, want 3 steps", f.ran)
	}
	if f.ran[0] != 0 {
		t.Errorf("first step was %d, want the monitor (0)", f.ran[0])
	}
}

// The failure that would strand someone: the monitor did not move, so the
// devices must not move either.
func TestFailedMonitorLeavesDevicesAlone(t *testing.T) {
	f := &fake{runErr: map[int]error{0: errors.New("Display not found")}}
	res := Switch(context.Background(), f, cfgWith("3"), "dp", false)
	if res.Code != CodeDisplayFailed {
		t.Errorf("code = %d, want %d", res.Code, CodeDisplayFailed)
	}
	if len(f.ran) != 1 {
		t.Fatalf("ran %v, want only the monitor", f.ran)
	}
	if !strings.Contains(res.Message, "left where they were") {
		t.Errorf("message = %q", res.Message)
	}
}

// Half-switched devices are worse than none, so the sequence stops at the first
// device failure. Here that is the first device, so nothing has moved.
func TestDeviceFailureStopsTheRest(t *testing.T) {
	f := &fake{runErr: map[int]error{1: errors.New("no such host")}}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", false)
	if res.Code != CodeDevicesFailed {
		t.Errorf("code = %d, want %d", res.Code, CodeDevicesFailed)
	}
	if len(f.ran) != 2 {
		t.Errorf("ran %v, want to stop before the second device", f.ran)
	}
	if res.Code == CodeDisplayFailed {
		t.Error("the two failure modes must not share a code")
	}
	// Nothing left this machine, so the keyboard and mouse are still here and
	// there is nothing to recover from. Sending someone to press the button
	// underneath would be sending them for no reason.
	if !strings.Contains(res.Message, "stayed here") {
		t.Errorf("message = %q, want it to say nothing left", res.Message)
	}
	if strings.Contains(res.Message, "way back") {
		t.Errorf("message = %q, want it not to point at a button when nothing moved", res.Message)
	}
}

// The other half: one device left and a later one did not, so something is
// missing and the button underneath is how it comes back.
func TestAPartialDeviceFailurePointsAtTheButton(t *testing.T) {
	f := &fake{runErr: map[int]error{2: errors.New("no such host")}}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", false)
	if res.Code != CodeDevicesFailed {
		t.Fatalf("code = %d, want %d", res.Code, CodeDevicesFailed)
	}
	if len(f.ran) != 3 {
		t.Errorf("ran %v, want it to stop at the second device", f.ran)
	}
	if !strings.Contains(res.Message, "way back") {
		t.Errorf("message = %q, want it to say how to recover", res.Message)
	}
}

// Both messages have to fit the eighty characters a panel shows, because a
// message cut off is one whose last words nobody reads.
func TestDeviceFailureMessagesFitAPanel(t *testing.T) {
	for _, ran := range []map[int]error{
		{1: errors.New("no such host")},
		{2: errors.New("no such host")},
	} {
		res := Switch(context.Background(), &fake{runErr: ran}, cfgWith("2"), "dp", false)
		if n := len([]rune(res.Message)); n > 80 {
			t.Errorf("message is %d characters, want at most 80: %q", n, res.Message)
		}
	}
}

func TestHostOffRunsOneStep(t *testing.T) {
	f := &fake{}
	if res := Switch(context.Background(), f, cfgWith("0"), "dp", false); res.Code != CodeOK {
		t.Fatalf("code = %d (%s)", res.Code, res.Message)
	}
	if len(f.ran) != 1 {
		t.Fatalf("ran %v, want only the monitor", f.ran)
	}
}

// With the flag set, nothing runs.
func TestDryRunExecutesNothing(t *testing.T) {
	f := &fake{}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", true)
	if res.Code != CodeOK {
		t.Fatalf("code = %d (%s)", res.Code, res.Message)
	}
	if len(f.ran) != 0 {
		t.Fatalf("dry run executed %v", f.ran)
	}
}

// A dry run has to show the commands, including a device name with spaces in
// it quoted, so it can be pasted into a shell to run by hand if wanted.
func TestDryRunPrintsTheCommands(t *testing.T) {
	f := &fake{}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", true)
	for _, want := range []string{
		"/usr/bin/ddcutil setvcp 60 0x0f --display 1",
		"/usr/bin/solaar config 'MX Keys S' change-host 2",
		"/usr/bin/solaar config 'LIFT VERTICAL ERGONOMIC MOUSE' change-host 2",
	} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("dry run output missing %q\ngot:\n%s", want, res.Message)
		}
	}
}

func TestDryRunWithHostOffShowsOnlyTheMonitor(t *testing.T) {
	f := &fake{}
	res := Switch(context.Background(), f, cfgWith("0"), "dp", true)
	if strings.Contains(res.Message, "solaar") {
		t.Errorf("dry run mentioned solaar with host off:\n%s", res.Message)
	}
}

func TestMissingBinaryHasItsOwnCode(t *testing.T) {
	f := &fake{runErr: map[int]error{0: backend.ErrMissing}}
	if res := Switch(context.Background(), f, cfgWith("2"), "dp", false); res.Code != CodeMissing {
		t.Errorf("display missing: code = %d, want %d", res.Code, CodeMissing)
	}
	f2 := &fake{runErr: map[int]error{1: backend.ErrMissing}}
	res := Switch(context.Background(), f2, cfgWith("2"), "dp", false)
	if res.Code != CodeMissing {
		t.Errorf("devices missing: code = %d, want %d", res.Code, CodeMissing)
	}
	if !strings.Contains(res.Message, "stayed here") {
		t.Errorf("message = %q", res.Message)
	}
}

func TestUnknownInputRunsNothing(t *testing.T) {
	f := &fake{}
	if res := Switch(context.Background(), f, cfgWith("2"), "scart", false); res.Code != CodeUsage {
		t.Errorf("code = %d, want %d", res.Code, CodeUsage)
	}
	if len(f.ran) != 0 {
		t.Errorf("ran %v", f.ran)
	}
}

func TestUnnamedInputFallsBackToKey(t *testing.T) {
	c := cfgWith("2")
	c.Inputs[1].Name = ""
	res := Switch(context.Background(), &fake{}, c, "dp", false)
	if !strings.Contains(res.Message, "dp") {
		t.Errorf("message = %q, want the key", res.Message)
	}
}

func TestPlanErrorIsAUsageError(t *testing.T) {
	f := &fake{planErr: errors.New("input source 999 is out of range")}
	if res := Switch(context.Background(), f, cfgWith("2"), "dp", false); res.Code != CodeUsage {
		t.Errorf("code = %d, want %d", res.Code, CodeUsage)
	}
}

func TestEmptyPlanRunsNothing(t *testing.T) {
	f := &fake{plan: backend.Plan{Platform: "fake"}}
	if res := Switch(context.Background(), f, cfgWith("2"), "dp", false); res.Code != CodeUsage {
		t.Errorf("code = %d, want %d", res.Code, CodeUsage)
	}
	if len(f.ran) != 0 {
		t.Errorf("ran %v", f.ran)
	}
}

// The message has to name the program that is actually missing.
//
// It said "ddcutil" on every platform. On a Mac that sent someone looking for a
// Linux program they had never heard of, while the one they had in fact
// installed, ddcctl, was the one being missed. A wrong name here costs more
// than a wrong name usually does, because it is read at the moment nothing
// works and the obvious next step is to go and install the wrong thing.
func TestTheMissingProgramIsNamedFromTheStepThatFailed(t *testing.T) {
	cases := []struct {
		bin  string
		want string
	}{
		{"/usr/bin/ddcutil", "ddcutil"},
		{"/opt/homebrew/bin/ddcctl", "ddcctl"},
		{"/usr/local/bin/ddcctl", "ddcctl"},
	}
	for _, c := range cases {
		f := &fake{
			plan: backend.Plan{Platform: "test", Steps: []backend.Step{
				{Bin: c.bin, Args: []string{"-d", "1"}},
			}},
			// The real backend wraps the sentinel with the binary it tried,
			// which is where the path in the detail comes from.
			runErr: map[int]error{
				0: fmt.Errorf("%s: %w", c.bin, backend.ErrMissing),
			},
		}
		res := Switch(context.Background(), f, cfgWith("0"), "dp", false)
		if res.Code != CodeMissing {
			t.Fatalf("%s: code = %d, want %d", c.bin, res.Code, CodeMissing)
		}
		if !strings.Contains(res.Message, c.want) {
			t.Errorf("%s: message = %q, want it to name %s", c.bin, res.Message, c.want)
		}
		// And it must not name a program that is not the one that failed.
		other := "ddcutil"
		if c.want == "ddcutil" {
			other = "ddcctl"
		}
		if strings.Contains(res.Message, other) {
			t.Errorf("%s: message = %q, want it not to name %s", c.bin, res.Message, other)
		}
		// The path is in the detail, so where it looked is answerable from the
		// output rather than by guessing.
		if !strings.Contains(res.Detail, c.bin) {
			t.Errorf("%s: detail = %q, want the path it looked for", c.bin, res.Detail)
		}
	}
}

// The device half has the same rule: name the program, not a category.
func TestTheMissingDeviceProgramIsNamedToo(t *testing.T) {
	f := &fake{
		plan: backend.Plan{Platform: "test", Steps: []backend.Step{
			{Bin: "/opt/homebrew/bin/ddcctl", Args: []string{"-d", "1", "-i", "15"}},
			{Bin: "/usr/local/bin/mxswitch", Args: []string{"2"}},
		}},
		runErr: map[int]error{
			1: fmt.Errorf("%s: %w", "/usr/local/bin/mxswitch", backend.ErrMissing),
		},
	}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", false)
	if res.Code != CodeMissing {
		t.Fatalf("code = %d, want %d", res.Code, CodeMissing)
	}
	if !strings.Contains(res.Message, "mxswitch") {
		t.Errorf("message = %q, want it to name mxswitch", res.Message)
	}
	if !strings.Contains(res.Message, "stayed here") {
		t.Errorf("message = %q, want it to say where the keyboard is", res.Message)
	}
}

// The normal sequence on a platform that moves one device per call: the first
// step moves one, the second moves another, the third finds nothing left.
//
// The third is not a failure. Reporting it as one says the keyboard stayed when
// it had just left, which is the opposite of what happened.
func TestAStepWithNothingLeftIsTheEndNotAFailure(t *testing.T) {
	f := &fake{runErr: map[int]error{2: backend.ErrNothingToDo}}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", false)
	if res.Code != CodeOK {
		t.Fatalf("code = %d (%s), want success: the devices were sent", res.Code, res.Message)
	}
	if len(f.ran) != 3 {
		t.Errorf("ran steps %v, want it to stop at the one with nothing to do", f.ran)
	}
}

// With nothing moved yet, the same message means something else: no device
// could be reached at all, and that is worth reporting rather than swallowing.
func TestNothingToDoWithNothingMovedIsAFailure(t *testing.T) {
	f := &fake{runErr: map[int]error{1: backend.ErrNothingToDo}}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", false)
	if res.Code != CodeDevicesFailed {
		t.Errorf("code = %d, want %d", res.Code, CodeDevicesFailed)
	}
	if len(f.ran) != 2 {
		t.Errorf("ran %v, want it to stop at the first device", f.ran)
	}
}

// A real failure still stops the sequence and still says the devices did not
// move, even when one already has. Half-moved is the case that strands people.
func TestARealFailureAfterASuccessStillStops(t *testing.T) {
	f := &fake{runErr: map[int]error{1: errors.New("no such host")}}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", false)
	if res.Code != CodeDevicesFailed {
		t.Errorf("code = %d, want %d", res.Code, CodeDevicesFailed)
	}
	if len(f.ran) != 2 {
		t.Errorf("ran %v, want it to stop at the failure", f.ran)
	}
	if !strings.Contains(res.Message, "stayed here") {
		t.Errorf("message = %q, want it to say nothing left", res.Message)
	}
	if strings.Contains(res.Message, "way back") {
		t.Errorf("message = %q, want it not to point at a button when nothing moved", res.Message)
	}
}

// And a missing program is still its own code, not swallowed as "nothing to do".
func TestAMissingProgramIsNotNothingToDo(t *testing.T) {
	f := &fake{runErr: map[int]error{1: backend.ErrMissing}}
	res := Switch(context.Background(), f, cfgWith("2"), "dp", false)
	if res.Code != CodeMissing {
		t.Errorf("code = %d, want %d", res.Code, CodeMissing)
	}
}

// The action runs last, after the monitor and after the devices. Sleeping or
// switching off first would leave the display never moved and the keyboard and
// mouse still here, which is the one ordering this program exists to prevent.
func TestPowerRunsAfterEverythingElse(t *testing.T) {
	f := &fake{power: []backend.Step{{Bin: "/usr/bin/systemctl", Args: []string{"suspend"}}}}
	res := Switch(context.Background(), f, cfgWithPower("2", config.PowerSleep), "dp", false)
	if res.Code != CodeOK {
		t.Fatalf("code = %d (%s)", res.Code, res.Message)
	}
	if len(f.ran) != 3 {
		t.Fatalf("ran move steps %v, want the monitor and two devices", f.ran)
	}
	if len(f.after) != 1 || f.after[0] != "/usr/bin/systemctl suspend" {
		t.Errorf("action steps = %v, want it run once after the moves", f.after)
	}
}

// A display that did not move means the user is still at this machine, so it
// must not be told to sleep or switch off.
func TestPowerDoesNotRunIfTheDisplayFailed(t *testing.T) {
	f := &fake{
		power:  []backend.Step{{Bin: "/usr/bin/systemctl", Args: []string{"suspend"}}},
		runErr: map[int]error{0: errors.New("no display")},
	}
	res := Switch(context.Background(), f, cfgWithPower("2", config.PowerSleep), "dp", false)
	if res.Code != CodeDisplayFailed {
		t.Errorf("code = %d, want %d", res.Code, CodeDisplayFailed)
	}
	if len(f.after) != 0 {
		t.Errorf("the machine was told to %v after the display failed to move", f.after)
	}
}

// The devices were not sent either, so the keyboard and mouse are still here.
func TestPowerDoesNotRunIfADeviceFailed(t *testing.T) {
	f := &fake{
		power:  []backend.Step{{Bin: "/usr/bin/systemctl", Args: []string{"suspend"}}},
		runErr: map[int]error{1: errors.New("no such host")},
	}
	res := Switch(context.Background(), f, cfgWithPower("2", config.PowerSleep), "dp", false)
	if res.Code != CodeDevicesFailed {
		t.Errorf("code = %d, want %d", res.Code, CodeDevicesFailed)
	}
	if len(f.after) != 0 {
		t.Errorf("the machine was told to %v after the devices failed to move", f.after)
	}
}

// The one failure where the keyboard and mouse are where the user wanted them.
// It gets its own exit code, because exit 3 sends someone looking for a keyboard
// that is not lost.
func TestAFailedActionIsItsOwnCode(t *testing.T) {
	f := &fake{
		power:    []backend.Step{{Bin: "/usr/bin/systemctl", Args: []string{"suspend"}}},
		powerErr: errors.New("Interactive authentication required"),
	}
	res := Switch(context.Background(), f, cfgWithPower("2", config.PowerSleep), "dp", false)
	if res.Code != CodePowerFailed {
		t.Fatalf("code = %d, want %d", res.Code, CodePowerFailed)
	}
	if res.Code == CodeDevicesFailed {
		t.Error("a failed action must not share the code that means a lost keyboard")
	}
	if !strings.Contains(res.Message, "did not sleep") {
		t.Errorf("message = %q, want it to say what did not happen", res.Message)
	}
	if !strings.Contains(res.Message, "Sent ") {
		t.Errorf("message = %q, want it to say the switch itself worked", res.Message)
	}
	// And the reason is kept, because a refusal to power off is worth reading.
	if !strings.Contains(res.Detail, "authentication") {
		t.Errorf("detail = %q, want the reason", res.Detail)
	}
}

func TestAShutdownSaysShutDown(t *testing.T) {
	f := &fake{
		power:    []backend.Step{{Bin: "/usr/bin/pmset", Args: []string{"sleepnow"}}},
		powerErr: errors.New("not permitted"),
	}
	res := Switch(context.Background(), f, cfgWithPower("2", config.PowerShutdown), "dp", false)
	if res.Code != CodePowerFailed {
		t.Fatalf("code = %d, want %d", res.Code, CodePowerFailed)
	}
	if !strings.Contains(res.Message, "did not shut down") {
		t.Errorf("message = %q, want it to say shutting down failed", res.Message)
	}
}

// The dry run has to show the action, or the one command that cannot be undone
// is the one nobody sees before running it.
func TestTheDryRunShowsTheAction(t *testing.T) {
	f := &fake{power: []backend.Step{{Bin: "/usr/bin/systemctl", Args: []string{"poweroff"}}}}
	res := Switch(context.Background(), f, cfgWithPower("2", config.PowerShutdown), "dp", true)
	if len(f.ran) != 0 || len(f.after) != 0 {
		t.Fatalf("a dry run ran %v and %v", f.ran, f.after)
	}
	if !strings.Contains(res.Message, "systemctl poweroff") {
		t.Errorf("dry run printed %q, want the action", res.Message)
	}
}

// Nothing set means nothing happens, and the run is exactly what it was before
// this existed.
func TestNoActionRunsNothingExtra(t *testing.T) {
	f := &fake{}
	res := Switch(context.Background(), f, cfgWithPower("2", config.PowerNone), "dp", false)
	if res.Code != CodeOK {
		t.Fatalf("code = %d", res.Code)
	}
	if len(f.ran) != 3 {
		t.Errorf("ran %v, want the monitor and two devices only", f.ran)
	}
	if len(f.after) != 0 {
		t.Errorf("ran %v, want nothing after the devices", f.after)
	}
}
