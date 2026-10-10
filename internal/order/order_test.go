package order

import (
	"context"
	"errors"
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
	planErr error
	runErr  map[int]error // keyed by step index
	ran     []int
}

func (f *fake) Platform() string { return "fake" }

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
	f.ran = append(f.ran, -1)
	return nil
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
// device failure.
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
	if !strings.Contains(res.Message, "way back") {
		t.Errorf("message = %q, want it to say how to recover", res.Message)
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

// The whole point of the flag: nothing runs.
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
