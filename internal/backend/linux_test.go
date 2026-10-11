//go:build linux

package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The argv arrays are the contract with the hardware, so they are checked
// literally. If one of these changes, the reason belongs in the commit.

// The monitor is always the first step, and the display number is ddcutil's own
// index from `ddcutil detect`, not the one Hyprland uses.
func TestLinuxPlanWritesHexForDdcutil(t *testing.T) {
	l := Linux{Names: []string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"}}
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
	l := Linux{Names: []string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"}}
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
	// The order is the list order, not one chosen here: the names are free
	// text, so nothing can tell a mouse from a keyboard except the person who
	// typed them. Reordering would be a guess dressed as a rule.
	if plan.Steps[1].Args[1] != "MX Keys S" || plan.Steps[2].Args[1] != "LIFT VERTICAL ERGONOMIC MOUSE" {
		t.Errorf("order = %q, %q; want it to match the list",
			plan.Steps[1].Args[1], plan.Steps[2].Args[1])
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
	l := Linux{Names: []string{"", "MX Keys S", ""}}
	plan, _ := l.Plan(1, 15, "1")
	if len(plan.Steps) != 2 {
		t.Errorf("steps = %d, want the monitor plus one real device", len(plan.Steps))
	}
}

// Plan must not execute anything. This is what makes --dry-run honest: the same
// code path builds the steps, and the only difference is that nothing is run.
func TestLinuxPlanRunsNothing(t *testing.T) {
	l := Linux{Names: []string{"MX Keys S"}}
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
	l := Linux{Names: []string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"}}
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

// Captured while the Dell was not answering over I2C. Every connector came back
// "Invalid display", and ddcutil printed no numbers at all.
func TestParseDisplaysWhenNoneAreAddressable(t *testing.T) {
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
   This monitor does not support DDC/CI. (I2C slave address x37 is unresponsive.)
`
	got := parseDisplays(real)
	if len(got) != 2 {
		t.Fatalf("displays = %d, want 2", len(got))
	}
	for _, d := range got {
		if d.Usable {
			t.Errorf("%s reported addressable, but ddcutil printed no number for it", d.Name)
		}
		if d.Number != 0 {
			t.Errorf("%s got number %d, want 0: ddcutil printed none", d.Name, d.Number)
		}
	}
	// The reason is kept, because a panel that cannot do DDC/CI and a monitor
	// that is merely not answering are different problems.
	if !strings.Contains(got[1].Why, "unresponsive") {
		t.Errorf("Why = %q, want the unresponsive reason", got[1].Why)
	}
	if len(Addressable(got)) != 0 {
		t.Errorf("Addressable = %v, want none", Addressable(got))
	}
}

// Captured once the Dell started answering, which is the case that broke.
//
// ddcutil numbers only the displays it will accept. The laptop panel is
// rejected and gets no number, so the Dell is "Display 1" and not "Display 2".
// Counting blocks positionally named a display ddcutil does not have, and told
// the user to change a config that was already right.
func TestParseDisplaysUsesDdcutilsOwnNumber(t *testing.T) {
	const real = `Invalid display
   I2C bus:  /dev/i2c-4
   DRM_connector:           card1-eDP-1
   EDID synopsis:
      Model:                Color LCD
   This is a laptop display.  Laptop displays do not support DDC/CI.

Display 1
   I2C bus:  /dev/i2c-19
   DRM_connector:           card2-DP-6
   EDID synopsis:
      Model:                DELL S2725DC
   VCP version:         2.1
`
	got := parseDisplays(real)
	if len(got) != 2 {
		t.Fatalf("displays = %d, want 2", len(got))
	}
	if got[0].Usable || got[0].Number != 0 {
		t.Errorf("the laptop panel must be unnumbered and unusable, got %+v", got[0])
	}
	if !got[1].Usable {
		t.Errorf("the Dell must be usable, got Why %q", got[1].Why)
	}
	if got[1].Number != 1 {
		t.Errorf("the Dell is display %d, want 1: ddcutil said so", got[1].Number)
	}
	if got[1].Name != "card2-DP-6" {
		t.Errorf("connector = %q", got[1].Name)
	}
	usable := Addressable(got)
	if len(usable) != 1 || usable[0].Number != 1 {
		t.Errorf("Addressable = %+v, want just display 1", usable)
	}
}

// Two addressable displays, so the numbers are 1 and 2 whichever way the
// invalid blocks are interspersed.
func TestParseDisplaysNumbersAcrossSeveralBlocks(t *testing.T) {
	const real = `Invalid display
   DRM_connector:           card1-eDP-1
   This is a laptop display.

Display 1
   DRM_connector:           card2-DP-6
   Model:                DELL S2725DC

Display 2
   DRM_connector:           card3-HDMI-A-1
   Model:                DELL U2723QE
`
	got := parseDisplays(real)
	if len(got) != 3 {
		t.Fatalf("displays = %d, want 3", len(got))
	}
	want := []int{0, 1, 2}
	for i, w := range want {
		if got[i].Number != w {
			t.Errorf("display %d numbered %d, want %d", i, got[i].Number, w)
		}
	}
	if len(Addressable(got)) != 2 {
		t.Errorf("Addressable = %v, want 2", Addressable(got))
	}
}

// An addressable display must not be reported as broken, or doctor would send
// people chasing a problem that is not there.
func TestParseDisplaysAcceptsAWorkingDisplay(t *testing.T) {
	const ok = `Display 1
   DRM_connector:           card2-DP-6
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

// A verdict this parser cannot read must not be counted as addressable, since a
// guessed number is exactly the failure being fixed.
func TestParseDisplaysRefusesAnUnreadableNumber(t *testing.T) {
	const odd = `Display one
   DRM_connector:           card2-DP-6
   Model:                DELL S2725DC
`
	got := parseDisplays(odd)
	if len(got) != 1 {
		t.Fatalf("displays = %d, want 1", len(got))
	}
	if got[0].Usable || got[0].Number != 0 {
		t.Errorf("got %+v, want unusable with no number", got[0])
	}
	if !strings.Contains(got[0].Why, "unreadable") {
		t.Errorf("Why = %q, want it to say the number was unreadable", got[0].Why)
	}
}

func TestParseDisplaysHandlesNothing(t *testing.T) {
	if got := parseDisplays(""); len(got) != 0 {
		t.Errorf("empty input gave %d displays", len(got))
	}
	if got := parseDisplays("\n\n  \n"); len(got) != 0 {
		t.Errorf("blank input gave %d displays", len(got))
	}
	if len(Addressable(nil)) != 0 {
		t.Error("Addressable(nil) should be empty")
	}
}

// A child that never comes back must be reported as such. exec.CommandContext
// kills it on expiry and Wait then hands back an *exec.ExitError with code -1,
// so checking the exit error first would make every timeout read "exited with
// -1" and say nothing about a program that never finished.
func TestATimeoutSaysSoRatherThanExitingWithMinusOne(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hang")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := runStep(context.Background(), Step{Bin: script}, 300*time.Millisecond)
	if err == nil {
		t.Fatal("a child that hangs did not fail")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s, the timeout did not fire", elapsed)
	}
	msg := err.Error()
	if strings.Contains(msg, "-1") {
		t.Errorf("error = %q, want the timeout rather than an exit code", msg)
	}
	if !strings.Contains(msg, "did not finish") {
		t.Errorf("error = %q, want it to say the child did not finish", msg)
	}
}

// Both streams at once, which is what a program reporting a failure does. This
// is the case that raced: os/exec uses a goroutine per stream.
func TestBothStreamsAreCollectedWithoutRacing(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "noisy")
	body := "#!/bin/sh\nfor i in $(seq 1 300); do echo \"out $i\"; echo \"err $i\" >&2; done\nexit 3\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	text, err := runStepRaw(context.Background(), Step{Bin: script}, ddcTimeout)
	if err == nil {
		t.Fatal("expected a failure from exit 3")
	}
	// Whatever the interleaving, both streams have to be there, or the message
	// explaining what happened would be missing half of it.
	if !strings.Contains(text, "out 300") {
		t.Error("stdout is missing")
	}
	if !strings.Contains(text, "err 300") {
		t.Error("stderr is missing")
	}
}

// doctor must agree with what runStep will actually find, or it reports ready
// for a switch that then says the program is not installed.
func TestInstalledAgreesWithWhatRunFinds(t *testing.T) {
	dir := t.TempDir()
	notExecutable := filepath.Join(dir, "data")
	if err := os.WriteFile(notExecutable, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Installed(notExecutable) {
		t.Error("a non-executable file is not installed")
	}
	executable := filepath.Join(dir, "prog")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !Installed(executable) {
		t.Error("an executable file is installed")
	}
	if Installed(filepath.Join(dir, "nope")) {
		t.Error("a missing file is not installed")
	}
}

// An empty channel means the settings file had no "host" for this input. That
// used to produce `solaar config '<device>' change-host ”`, which runs, fails,
// and has already moved the monitor by then. It is refused before the monitor
// moves instead, which is the direction that costs nobody their keyboard.
func TestLinuxRefusesAnEmptyChannel(t *testing.T) {
	l := Linux{Names: []string{"MX Keys S"}}
	if _, err := l.Plan(1, 15, ""); err == nil {
		t.Fatal("an empty channel was accepted")
	}
	// And nothing partial comes back with the error.
	plan, err := l.Plan(1, 15, "")
	if err != nil && len(plan.Steps) != 0 {
		t.Errorf("a refused plan has %d steps, want none", len(plan.Steps))
	}
}

func TestLinuxRefusesAChannelOutsideOneToThree(t *testing.T) {
	l := Linux{Names: []string{"MX Keys S"}}
	for _, channel := range []string{"4", "9", "-1", "x", " 2", "2 "} {
		if _, err := l.Plan(1, 15, channel); err == nil {
			t.Errorf("accepted channel %q", channel)
		}
	}
	// Off is not an error: it means no device step at all.
	plan, err := l.Plan(1, 15, "0")
	if err != nil {
		t.Fatalf("off was refused: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("off produced %d steps, want only the monitor", len(plan.Steps))
	}
}

// Both backends have to agree about which channels exist, or a settings file
// that works on one machine refuses on the other.
func TestBothBackendsAgreeOnChannels(t *testing.T) {
	for _, channel := range []string{"0", "1", "2", "3", "4", "", "x", "-1"} {
		err := validChannel(channel)
		// Only 1, 2 and 3 are channels; the caller handles "0" before this.
		want := channel == "1" || channel == "2" || channel == "3"
		if (err == nil) != want {
			t.Errorf("validChannel(%q) error = %v, want it to be accepted: %v", channel, err, want)
		}
	}
}
