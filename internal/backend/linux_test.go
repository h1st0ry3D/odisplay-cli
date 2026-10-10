//go:build linux

package backend

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The argv arrays are the contract with the hardware, so they are checked
// literally. If one of these changes, the reason belongs in the commit.

// The monitor is always the first step, and the display number is ddcutil's own
// index from `ddcutil detect`, not the one Hyprland uses.
func TestLinuxPlanWritesHexForDdcutil(t *testing.T) {
	l := Linux{Devices: []string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"}}
	plan, err := l.Plan(1, 15, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 with host off", len(plan.Steps))
	}
	want := Step{Bin: "/usr/bin/ddcutil", Args: []string{"setvcp", "60", "0x0f", "--display", "1"}}
	if !reflect.DeepEqual(plan.Steps[0], want) {
		t.Errorf("step = %+v, want %+v", plan.Steps[0], want)
	}
}

// Solaar addresses a device by the name it prints, and a name that does not
// match exactly is an error rather than a silent no-op. So it gets one call per
// device, and the name is one argument with its spaces intact.
func TestLinuxPlanIsOneStepPerDevice(t *testing.T) {
	l := Linux{Devices: []string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"}}
	plan, err := l.Plan(1, 15, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("steps = %d, want the monitor plus 2 devices", len(plan.Steps))
	}
	want := []string{"config", "MX Keys S", "change-host", "2"}
	if !reflect.DeepEqual(plan.Steps[1].Args, want) {
		t.Errorf("args = %q, want %q", plan.Steps[1].Args, want)
	}
	// The mouse is last, so the pointer is still here if the keyboard move is
	// what went wrong.
	if !strings.Contains(plan.Steps[2].Args[1], "LIFT") {
		t.Errorf("last device = %q, want the mouse", plan.Steps[2].Args[1])
	}
}

func TestLinuxPlanRefusesAnOutOfRangeValue(t *testing.T) {
	if _, err := (Linux{}).Plan(1, 999, "0"); err == nil {
		t.Error("accepted 999")
	}
	if _, err := (Linux{}).Plan(1, -1, "0"); err == nil {
		t.Error("accepted -1")
	}
}

// An empty device name in the config would otherwise become a Solaar call that
// matches nothing and reports an error that reads like the keyboard is gone.
func TestEmptyDeviceNamesAreSkipped(t *testing.T) {
	l := Linux{Devices: []string{"", "MX Keys S", ""}}
	plan, _ := l.Plan(1, 15, "1")
	if len(plan.Steps) != 2 {
		t.Errorf("steps = %d, want the monitor plus one real device", len(plan.Steps))
	}
}

// Plan must not execute anything. This is what makes --dry-run honest: the same
// code path builds the steps, and the only difference is that nothing is run.
func TestLinuxPlanRunsNothing(t *testing.T) {
	l := Linux{Devices: []string{"MX Keys S"}}
	plan, err := l.Plan(1, 15, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("steps = %d", len(plan.Steps))
	}
}

// Host 0 means the devices stay where they are, so there is one step and no
// Solaar call at all.
func TestLinuxHostOffIsOneStep(t *testing.T) {
	l := Linux{Devices: []string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"}}
	plan, _ := l.Plan(1, 15, "0")
	if len(plan.Steps) != 1 {
		t.Errorf("steps = %d, want only the monitor", len(plan.Steps))
	}
}

// Running a step against a program that is not installed says so, rather than
// looking like the program refused. The caller turns that into its own exit
// code.
func TestRunReportsAMissingProgram(t *testing.T) {
	l := Linux{}
	err := l.Run(context.Background(), Step{Bin: "/usr/bin/definitely-not-here"})
	if err == nil {
		t.Fatal("Run succeeded against a missing binary")
	}
	if !errors.Is(err, ErrMissing) {
		t.Errorf("error = %q, want it to wrap ErrMissing", err)
	}
}

// New must return the Linux backend, not the refusing fallback. This was
// wrong once: a build that compiled fine and refused to switch anything, because
// the per-platform constructor was missing and the fallback answered instead.
func TestNewReturnsTheLinuxBackend(t *testing.T) {
	b := New([]string{"MX Keys S"})
	if _, ok := b.(Linux); !ok {
		t.Fatalf("New() = %T, want Linux", b)
	}
	if got := b.Platform(); got != "linux" {
		t.Errorf("Platform() = %q, want linux", got)
	}
}

// The device names have to survive into the plan, because Solaar matches on
// them exactly.
func TestNewKeepsTheDeviceNames(t *testing.T) {
	b := New([]string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"})
	plan, err := b.Plan(1, 15, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("steps = %d, want the monitor plus 2 devices", len(plan.Steps))
	}
	if plan.Steps[1].Args[1] != "MX Keys S" {
		t.Errorf("device name lost: %q", plan.Steps[1].Args[1])
	}
}

// Captured from this machine. The point of the test is the two numbers: ddcutil
// does not print them, and a laptop panel sitting in the list is what shifts
// the monitor's number away from 1.
func TestParseDisplaysNumbersInOrderAndKeepsTheReason(t *testing.T) {
	const real = `Invalid display
   I2C bus:  /dev/i2c-4
   DRM_connector:           card1-eDP-1
   EDID synopsis:
      Mfg id:               APP - Apple Computer Inc
      Model:                Color LCD
   This is a laptop display.  Laptop displays do not support DDC/CI.

Invalid display
   I2C bus:  /dev/i2c-19
   DRM_connector:           card2-DP-6
   EDID synopsis:
      Mfg id:               DEL - Dell Inc.
      Model:                DELL S2725DC
      Product code:         61815  (0xf177)
   This monitor does not support DDC/CI. (I2C slave address x37 is unresponsive.)
`
	got := parseDisplays(real)
	if len(got) != 2 {
		t.Fatalf("displays = %d, want 2", len(got))
	}
	// Numbered by position, starting at 1.
	if got[0].Number != 1 || got[1].Number != 2 {
		t.Errorf("numbers = %d,%d want 1,2", got[0].Number, got[1].Number)
	}
	if got[1].Name != "card2-DP-6" {
		t.Errorf("connector = %q, want card2-DP-6", got[1].Name)
	}
	// The reason is kept, because a panel that cannot do DDC/CI and a monitor
	// that is merely not answering are different problems.
	if !strings.Contains(got[1].Why, "unresponsive") {
		t.Errorf("Why = %q, want the unresponsive reason", got[1].Why)
	}
	if got[0].Usable || got[1].Usable {
		t.Error("both were reported Invalid, so both must be unusable")
	}
}

// A usable display must not be reported as broken, or doctor would send people
// chasing a problem that is not there.
func TestParseDisplaysAcceptsAWorkingDisplay(t *testing.T) {
	const ok = `Display 1
   I2C bus:  /dev/i2c-19
   DRM_connector:           card2-DP-6
   EDID synopsis:
      Mfg id:               DEL - Dell Inc.
      Model:                DELL S2725DC
   VCP code 0x60 (Input Source):
      0f  DisplayPort-1
`
	got := parseDisplays(ok)
	if len(got) != 1 {
		t.Fatalf("displays = %d, want 1", len(got))
	}
	if !got[0].Usable {
		t.Errorf("a Display block must be usable, got Why %q", got[0].Why)
	}
	if got[0].Why != "" {
		t.Errorf("Why = %q, want empty for a working display", got[0].Why)
	}
}

func TestParseDisplaysHandlesNothing(t *testing.T) {
	if got := parseDisplays(""); len(got) != 0 {
		t.Errorf("empty input gave %d displays", len(got))
	}
	if got := parseDisplays("\n\n  \n"); len(got) != 0 {
		t.Errorf("blank input gave %d displays", len(got))
	}
}
