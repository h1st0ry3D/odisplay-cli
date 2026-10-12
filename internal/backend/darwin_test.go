//go:build darwin

package backend

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These only compile and run where Darwin exists. Run them on a Mac:
//   GOOS=darwin go vet ./...

// The display step is the first one, and its shape depends on which program
// this Mac has. m1ddc takes `display <n> set input <vcp>`; ddcctl takes
// `-d <n> -i <vcp>`. Both get the same number, written the way each wants it.
func TestDarwinPlanWritesTheMonitorInvocation(t *testing.T) {
	plan, err := Darwin{Count: 2}.Plan(1, 17, "3")
	if err != nil {
		t.Fatal(err)
	}
	wantM1 := Step{Bin: m1ddc, Args: []string{"display", "1", "set", "input", "17"}}
	wantDDC := Step{Bin: ddcctl, Args: []string{"-d", "1", "-i", "17"}}
	got := plan.Steps[0]
	if !reflect.DeepEqual(got, wantM1) && !reflect.DeepEqual(got, wantDDC) {
		t.Errorf("monitor step = %+v, want %+v or %+v", got, wantM1, wantDDC)
	}
}

// m1ddc is the one that works on Apple Silicon, so it wins when both are
// installed. ddcctl's DDC calls do nothing there while still reporting success.
func TestM1DDCIsPreferredOverDdcctl(t *testing.T) {
	if Installed(m1ddc) && Installed(ddcctl) {
		if got := monitorProgram(); got != m1ddc {
			t.Errorf("monitorProgram() = %q, want m1ddc", got)
		}
	}
}

// Whichever is picked, neither being present is an error that names what to
// install rather than one that names nothing.
func TestNoMonitorProgramIsRefusedWithAdvice(t *testing.T) {
	if monitorProgram() != "" {
		t.Skip("a monitor program is installed here")
	}
	if _, err := (Darwin{}).Plan(1, 15, "0"); err == nil {
		t.Fatal("a plan was built with no program to run")
	} else if !strings.Contains(err.Error(), "m1ddc") {
		t.Errorf("error = %q, want it to name what to install", err)
	}
}

// The macOS display list format, pinned to the source it comes from:
//
//	writeToStdOut([NSString stringWithFormat:@"[%i] %@ (%@)\n", ...]);
func TestParsingTheM1DDCDisplayList(t *testing.T) {
	const real = "[1] DELL S2725DC (0F0E1D2C-1111-2222-3333-444455556666)\n[2] LG HDR 4K (AAAA-...)\n"
	// The parse lives in listWithM1DDC, which runs the program, so the shape
	// is checked by driving the same code path with a stub on PATH.
	t.Setenv("PATH", stubM1DDC(t, real))
	got, err := listWithM1DDC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("displays = %d, want 2", len(got))
	}
	if got[0].Number != 1 || got[0].Name != "DELL S2725DC" {
		t.Errorf("first = %+v, want display 1 named DELL S2725DC", got[0])
	}
	if !got[0].Usable {
		t.Error("a listed display must be usable")
	}
}

// m1ddc prints nothing parseable and exits zero when there is no external
// display. An empty list would read as a machine with no monitors, which is a
// different and wrong thing to say.
func TestAnUnreadableDisplayListIsRefused(t *testing.T) {
	t.Setenv("PATH", stubM1DDC(t, ""))
	if _, err := listWithM1DDC(context.Background()); err == nil {
		t.Error("an empty listing was accepted as no displays")
	}
}

// One number, two spellings: USB-C is 27 to ddcctl and 0x1b to ddcutil.
func TestDarwinWritesTheValueInDecimal(t *testing.T) {
	plan, err := Darwin{}.Plan(1, 27, "0")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Steps[0].Args[3]; got != "27" {
		t.Errorf("value = %q, want 27", got)
	}
}

func TestDarwinHostOffIsOneStep(t *testing.T) {
	plan, err := Darwin{Count: 2}.Plan(1, 27, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %d, want only the monitor", len(plan.Steps))
	}
}

// mxswitch moves one device per call, so the number of calls is how many
// devices the config lists.
func TestDarwinMovesOneStepPerDevice(t *testing.T) {
	plan, err := Darwin{Count: 2}.Plan(1, 15, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("steps = %d, want the monitor plus 2 devices", len(plan.Steps))
	}
	want := Step{Bin: mxswitch, Args: []string{"2"}}
	for i := 1; i < 3; i++ {
		if !reflect.DeepEqual(plan.Steps[i], want) {
			t.Errorf("device step %d = %+v, want %+v", i, plan.Steps[i], want)
		}
	}
}

// A config with no devices must not silently claim to have moved them.
func TestDarwinWithNoDevicesMakesNoDeviceSteps(t *testing.T) {
	plan, err := Darwin{Count: 0}.Plan(1, 15, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("steps = %d, want only the monitor", len(plan.Steps))
	}
}

// mxswitch accepts only 1, 2 and 3. A channel it would refuse is caught here,
// where the devices are still where they are.
func TestDarwinRefusesAChannelMxswitchWouldReject(t *testing.T) {
	for _, channel := range []string{"0", "4", "9", "", "x"} {
		if _, err := (Darwin{Count: 1}).Plan(1, 15, channel); err == nil {
			// "0" is off, which is not an error, just one step.
			plan, _ := (Darwin{Count: 1}).Plan(1, 15, channel)
			if channel != "0" {
				t.Errorf("accepted channel %q", channel)
			} else if len(plan.Steps) != 1 {
				t.Errorf("channel 0 made %d steps, want only the monitor", len(plan.Steps))
			}
		}
	}
}

func TestDarwinPlanRefusesAnOutOfRangeValue(t *testing.T) {
	if _, err := (Darwin{}).Plan(1, 999, "0"); err == nil {
		t.Error("accepted 999")
	}
	if _, err := (Darwin{}).Plan(1, -1, "0"); err == nil {
		t.Error("accepted -1")
	}
}

// Homebrew puts things in /opt/homebrew/bin on Apple Silicon and
// /usr/local/bin on Intel. A single hardcoded path is wrong on half of Macs.
func TestMustFindFindsAProgramInEitherPrefix(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "thing")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := mustFind("thing", "/nowhere", dir); got != prog {
		t.Errorf("mustFind = %q, want %q", got, prog)
	}
	// Not executable, so not a program it can run.
	plain := filepath.Join(dir, "notexec")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustFind("notexec", "/nowhere", dir); got != filepath.Join("/nowhere", "notexec") {
		t.Errorf("mustFind = %q, want the first candidate when nothing is runnable", got)
	}
	// A directory is not a program either.
	if got := mustFind("thing", dir, "/nowhere"); got != filepath.Join(dir, "thing") {
		t.Errorf("mustFind = %q, want to skip the directory", got)
	}
}

// The count of configured devices is what the macOS plan uses, and Solaar's
// names mean nothing here.
func TestNewCountsDevicesAndIgnoresTheirNames(t *testing.T) {
	d := New([]string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"})
	darwin, ok := d.(Darwin)
	if !ok {
		t.Fatalf("New() = %T, want Darwin", d)
	}
	if darwin.Count != 2 {
		t.Errorf("Count = %d, want 2", darwin.Count)
	}
	if New([]string{"a", "", "  "}).(Darwin).Count != 1 {
		t.Error("a blank device name still counted")
	}
	if New(nil).(Darwin).Count != 0 {
		t.Error("no devices should mean no device steps")
	}
}

func TestDarwinDetectReportsAMissingDdcctl(t *testing.T) {
	d := Darwin{}
	if _, err := d.Detect(context.Background()); err == nil {
		t.Skip("ddcctl is installed here")
	}
}

// stubM1DDC puts a fake m1ddc on PATH that prints body, and points the
// resolved path at it.
func stubM1DDC(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "m1ddc")
	content := "#!/bin/sh\ncat <<'EOF'\n" + body + "EOF\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	// listWithM1DDC runs the resolved absolute path, which was worked out at
	// package init, so point it at the stub for this test.
	saved := m1ddc
	m1ddc = script
	t.Cleanup(func() { m1ddc = saved })
	return dir
}

func TestNewReturnsTheDarwinBackend(t *testing.T) {
	b := New(nil)
	if _, ok := b.(Darwin); !ok {
		t.Fatalf("New() = %T, want Darwin", b)
	}
	if got := b.Platform(); got != "darwin" {
		t.Errorf("Platform() = %q, want darwin", got)
	}
}

// The same case the Mac hit: a settings file with no channel for an input.
func TestDarwinRefusesAnEmptyChannel(t *testing.T) {
	if _, err := (Darwin{Count: 1}).Plan(1, 15, ""); err == nil {
		t.Fatal("an empty channel was accepted")
	}
}

func TestDarwinRefusesAChannelOutsideOneToThree(t *testing.T) {
	for _, channel := range []string{"4", "9", "-1", "x", " 2"} {
		if _, err := (Darwin{Count: 1}).Plan(1, 15, channel); err == nil {
			t.Errorf("accepted channel %q", channel)
		}
	}
}

// A no-sudo install puts these in the user's own bin, and that is exactly what
// the instructions for mxswitch say to do:
//
//	make install PREFIX="$HOME/.local"   ->  ~/.local/bin/mxswitch
//
// Leaving that directory out reported a program installed as documented as not
// installed at all. ddcctl had been put there by hand for the same reason.
func TestSearchDirsIncludeTheUsersOwnBin(t *testing.T) {
	t.Setenv("HOME", "/Users/someone")
	dirs := searchDirs()

	want := "/Users/someone/.local/bin"
	found := false
	for _, d := range dirs {
		if d == want {
			found = true
		}
	}
	if !found {
		t.Errorf("searchDirs() = %v, want %s among them", dirs, want)
	}

	// Homebrew on Apple Silicon, Homebrew on Intel, and the system, because a
	// machine can have any of them.
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} {
		ok := false
		for _, got := range dirs {
			if got == d {
				ok = true
			}
		}
		if !ok {
			t.Errorf("searchDirs() = %v, want %s among them", dirs, d)
		}
	}
}

// The order matters for the message when nothing is found: it names the first
// place it looked, which should be the one most people would try.
func TestSearchDirsPutHomebrewFirst(t *testing.T) {
	t.Setenv("HOME", "/Users/someone")
	if got := searchDirs()[0]; got != "/opt/homebrew/bin" {
		t.Errorf("first search directory = %q, want /opt/homebrew/bin", got)
	}
}

// No HOME is not a reason to search nowhere.
func TestSearchDirsSurviveNoHome(t *testing.T) {
	t.Setenv("HOME", "")
	dirs := searchDirs()
	if len(dirs) < 3 {
		t.Errorf("searchDirs() = %v, want the system directories at least", dirs)
	}
	for _, d := range dirs {
		if d == "" || d == "/.local/bin" {
			t.Errorf("searchDirs() = %v, has a nonsense entry", dirs)
		}
	}
}

func TestParsingMxswitchListWithNone(t *testing.T) {
	for _, text := range []string{"", "\n", "0xFF00:0x0001  in=20  USB Receiver\n"} {
		if got := parseMxswitchList(text); len(got) != 0 {
			t.Errorf("parseMxswitchList(%q) = %v, want none", text, got)
		}
	}
}

// The commands that put a Mac to sleep and switch it off have to be the ones
// macOS actually ships. They were written out from memory once, and pmset ended
// up in /usr/sbin, which does not exist on a Mac.
//
// Nothing on a Linux machine can catch that, and the symptom is a sleep that
// never happens on a machine you have already walked away from, so it is worth
// a test that can only run here.
func TestTheCommandsForSleepingAndShuttingDownAreThere(t *testing.T) {
	for _, action := range []string{"sleep", "shutdown"} {
		steps := (Darwin{}).Power(action)
		if len(steps) != 1 {
			t.Fatalf("%s is %d steps, want one", action, len(steps))
		}
		step := steps[0]
		if _, err := os.Stat(step.Bin); err != nil {
			t.Errorf("%s runs %s, which is not there: %v", action, step.Bin, err)
		}
		// sudo runs the command in its arguments, and that path is the one a
		// sudoers rule has to name, so a wrong one is wrong twice over.
		if step.Bin != sudo {
			continue
		}
		if len(step.Args) < 2 {
			t.Fatalf("%s has no command in it: %v", action, step.Args)
		}
		if _, err := os.Stat(step.Args[1]); err != nil {
			t.Errorf("%s runs %s, which is not there: %v", action, step.Args[1], err)
		}
	}
}
