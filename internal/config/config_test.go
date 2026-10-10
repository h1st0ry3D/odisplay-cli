package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "odisplay.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Inputs) != 3 {
		t.Fatalf("inputs = %d, want the 3 defaults", len(cfg.Inputs))
	}
	if cfg.Inputs[0].VCP != 27 {
		t.Errorf("usb-c vcp = %d, want 27", cfg.Inputs[0].VCP)
	}
}

// A file that will not parse is an error. Silently falling back would throw
// away a hand-edited setup over one stray comma.
func TestUnparseableFileIsAnError(t *testing.T) {
	path := write(t, "{not json")
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a file that does not parse")
	}
}

// A channel off the closed list is dropped, not repaired, so the number that
// reaches a backend is always one the panel can show.
func TestHostOffTheListIsDropped(t *testing.T) {
	path := write(t, `{"version":1,"inputs":[{"key":"dp","vcp":15,"host":"9"}]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Inputs[0].Host; got != HostOff {
		t.Errorf("host = %q, want it dropped to %q", got, HostOff)
	}
}

func TestHostAcceptsNumbersAndStrings(t *testing.T) {
	path := write(t, `{"version":1,"inputs":[
		{"key":"dp","vcp":15,"host":2},
		{"key":"hdmi","vcp":17,"host":"3"}]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Inputs[0].Host != "2" || cfg.Inputs[1].Host != "3" {
		t.Errorf("hosts = %q,%q want 2,3", cfg.Inputs[0].Host, cfg.Inputs[1].Host)
	}
}

// Repairing an impossible display number to 1 would point every switch at
// whichever monitor happens to be first, and report success for it.
func TestDisplayOutOfRangeIsRefusedNotRepaired(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"display":0}`,
		`{"version":1,"display":-3}`,
		`{"version":1,"display":100000}`,
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("Load accepted %s", body)
		}
	}
}

func TestDisplayInRangeIsKept(t *testing.T) {
	for _, n := range []int{1, 2, 7, 999} {
		cfg, err := Load(write(t, `{"version":1,"display":`+itoa(n)+`}`))
		if err != nil {
			t.Errorf("display %d: %v", n, err)
			continue
		}
		if cfg.Display != n {
			t.Errorf("display = %d, want %d", cfg.Display, n)
		}
	}
}

// A file with no display at all must not become display 0, which is not a
// display. Default() supplies 1 for a missing file, and a missing key is
// reported as such.
func TestMissingDisplayKey(t *testing.T) {
	cfg, err := Load(write(t, `{"version":1,"inputs":[{"key":"dp","vcp":15}]}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Display != 1 {
		t.Errorf("display = %d, want the default 1", cfg.Display)
	}
}

func TestVCPIsStoredAsDecimal(t *testing.T) {
	// ddcutil writes 0x1b, ddcctl writes 27. One number serves both.
	path := write(t, `{"version":1,"inputs":[{"key":"usbc","vcp":27}]}`)
	cfg, _ := Load(path)
	if cfg.Inputs[0].VCP != 27 {
		t.Errorf("vcp = %d, want 27", cfg.Inputs[0].VCP)
	}
}

func TestRoundTrip(t *testing.T) {
	path := write(t, `{"version":1,"display":2,"inputs":[{"key":"dp","name":"Mac Mini","vcp":15,"host":"3"}],"devices":["MX Keys S"]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetPath(path)
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Display != 2 || again.Inputs[0].Name != "Mac Mini" ||
		again.Inputs[0].Host != "3" || len(again.Devices) != 1 {
		t.Errorf("round trip lost something: %+v", again)
	}
}

func TestSaveCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "odisplay.json")
	cfg := Default()
	cfg.SetPath(path)
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestNextHostWraps(t *testing.T) {
	want := []string{"1", "2", "3", "0"}
	got := "0"
	for _, w := range want {
		got = NextHost(got)
		if got != w {
			t.Fatalf("NextHost = %q, want %q", got, w)
		}
	}
}

func TestCleanNameStripsControlAndCaps(t *testing.T) {
	if got := CleanName("a\nb\tc"); got != "abc" {
		t.Errorf("CleanName = %q, want abc", got)
	}
	if got := CleanName(strings.Repeat("x", 100)); len([]rune(got)) != MaxNameLength {
		t.Errorf("length = %d, want %d", len([]rune(got)), MaxNameLength)
	}
	if got := CleanName("  padded  "); got != "padded" {
		t.Errorf("CleanName = %q, want padded", got)
	}
}

// ODISPLAY_CONFIG is read as either the file or the directory holding it.
// Treating it as a directory only, when someone passes a file path, produced a
// path like /tmp/x.json/odisplay.json that could never exist and was not
// reported as missing until a switch was attempted.
func TestConfigEnvTakesAFileOrADirectory(t *testing.T) {
	cases := []struct{ set, want string }{
		{"/tmp/odisplay.json", "/tmp/odisplay.json"},
		{"/tmp/odisplay.JSON", "/tmp/odisplay.JSON"},
		{"/tmp/settings.json", "/tmp/settings.json"},
		{"/tmp/cfg", "/tmp/cfg/odisplay.json"},
		{"/tmp/cfg/", "/tmp/cfg/odisplay.json"},
	}
	for _, c := range cases {
		t.Setenv("ODISPLAY_CONFIG", c.set)
		got, err := Path()
		if err != nil {
			t.Errorf("Path() with %q: %v", c.set, err)
			continue
		}
		if got != c.want {
			t.Errorf("ODISPLAY_CONFIG=%q gave %q, want %q", c.set, got, c.want)
		}
	}
}

func TestPathFallsBackWithoutTheEnvVar(t *testing.T) {
	t.Setenv("ODISPLAY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/xdg/odisplay/odisplay.json"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

// A file that is present but broken must not resolve to the defaults, because
// the defaults name a display number and hosts the user did not choose.
func TestBrokenFileDoesNotResolveToDefaults(t *testing.T) {
	path := write(t, `{"version":1,"display":2,"inputs":`)
	cfg, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a truncated file")
	}
	if cfg.Display == 2 {
		t.Error("the broken value leaked through, so it looks like the real config")
	}
}

// encoding/json merges a JSON array into the slice it is given, field by field
// rather than replacing it. Unmarshalling over the defaults therefore handed
// every input the default name at its own position, so a file listing only dp
// and hdmi called them "USB-C" and "DisplayPort".
//
// A name belonging to one input must never turn up on another.
func TestAnAbsentNameDoesNotComeFromTheDefault(t *testing.T) {
	path := write(t, `{"version":1,"inputs":[
		{"key":"dp","vcp":15},
		{"key":"hdmi","vcp":17}]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inputs) != 2 {
		t.Fatalf("inputs = %d, want only the 2 in the file", len(cfg.Inputs))
	}
	// The defaults have usbc first, so position 0 used to inherit "USB-C".
	for _, in := range cfg.Inputs {
		if in.Name != "" {
			t.Errorf("%s picked up the name %q from the defaults", in.Key, in.Name)
		}
	}
}

func TestInputsComeOnlyFromTheFile(t *testing.T) {
	path := write(t, `{"version":1,"display":2,"inputs":[{"key":"hdmi","vcp":17,"host":"2"}]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inputs) != 1 || cfg.Inputs[0].Key != "hdmi" {
		t.Fatalf("inputs = %+v, want just hdmi", cfg.Inputs)
	}
	if cfg.Display != 2 {
		t.Errorf("display = %d, want 2", cfg.Display)
	}
}

// A file that says nothing about the devices keeps the defaults, because a
// missing list would otherwise mean Solaar moves nothing and says so only by
// failing.
func TestDevicesKeepTheirDefaultsWhenUnmentioned(t *testing.T) {
	path := write(t, `{"version":1,"inputs":[{"key":"dp","vcp":15}]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Devices) == 0 {
		t.Error("devices went missing, so the keyboard and mouse would never move")
	}
}

// A file with its own device list replaces the defaults rather than merging
// with them.
func TestDevicesInTheFileReplaceTheDefaults(t *testing.T) {
	path := write(t, `{"version":1,"inputs":[{"key":"dp","vcp":15}],"devices":["MX Keys S"]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0] != "MX Keys S" {
		t.Errorf("devices = %q, want just MX Keys S", cfg.Devices)
	}
}

// A file with no inputs key means no inputs, not the three defaults: the file is
// the whole truth once it exists.
func TestAFileWithNoInputsHasNoInputs(t *testing.T) {
	path := write(t, `{"version":1,"display":3}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inputs) != 0 {
		t.Errorf("inputs = %+v, want none", cfg.Inputs)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// The same monitor is a different index under ddcutil than under ddcctl, so one
// settings file shared by an Omarchy laptop and a Mac has to be able to carry
// both.
func TestDisplayNumberPrefersThisPlatform(t *testing.T) {
	c := Config{Display: 1, Displays: map[string]int{"linux": 3, "darwin": 2}}

	// Whichever platform this test is running on, its own entry wins.
	want := c.Displays[runtime.GOOS]
	if got := c.DisplayNumber(); got != want {
		t.Errorf("DisplayNumber() = %d, want %d for %s", got, want, runtime.GOOS)
	}

	// And the number meant for the other one is still readable.
	other := "linux"
	if runtime.GOOS == "linux" {
		other = "darwin"
	}
	if got := c.Displays[other]; got < 1 {
		t.Errorf("displays[%s] = %d, want it kept", other, got)
	}
}

func TestDisplayNumberFallsBackToTheSharedDefault(t *testing.T) {
	cases := []Config{
		{Display: 4},
		{Display: 4, Displays: map[string]int{}},
		// An entry for the other platform only.
		{Display: 4, Displays: map[string]int{"nonesuch": 9}},
	}
	for _, c := range cases {
		if got := c.DisplayNumber(); got != 4 {
			t.Errorf("DisplayNumber() = %d, want 4 for %+v", got, c)
		}
	}
}

// Setting the number on one machine must not move the other's, which is the
// whole reason this exists.
func TestSetDisplayNumberOnlyTouchesThisPlatform(t *testing.T) {
	c := Config{Display: 1, Displays: map[string]int{
		"linux": 1, "darwin": 5,
	}}
	other := "linux"
	if runtime.GOOS == "linux" {
		other = "darwin"
	}
	before := c.Displays[other]

	c.SetDisplayNumber(7)

	if got := c.DisplayNumber(); got != 7 {
		t.Errorf("DisplayNumber() = %d, want 7", got)
	}
	if got := c.Displays[other]; got != before {
		t.Errorf("displays[%s] = %d, want it left at %d", other, got, before)
	}
}

// A file carrying both a default and an identical entry is harder to read than
// one carrying either, so the redundant one goes.
func TestSetDisplayNumberDropsARedundantEntry(t *testing.T) {
	c := Config{Display: 1}
	c.SetDisplayNumber(1)
	if c.Displays != nil {
		t.Errorf("Displays = %v, want nil when it matches the default", c.Displays)
	}
	if c.DisplayNumber() != 1 {
		t.Errorf("DisplayNumber() = %d, want 1", c.DisplayNumber())
	}
}

func TestSetDisplayNumberKeepsAnotherPlatformsEntry(t *testing.T) {
	c := Config{Display: 1, Displays: map[string]int{"darwin": 2}}
	if runtime.GOOS != "darwin" {
		c.SetDisplayNumber(1)
		if got := c.Displays["darwin"]; got != 2 {
			t.Errorf("displays.darwin = %d, want 2 kept", got)
		}
		if c.Displays == nil {
			t.Error("the map was dropped even though it still holds another platform")
		}
	}
}

// "macos" rather than "darwin" would be ignored in silence, leaving the shared
// default in charge of a machine that may number its displays differently.
func TestAMisspeltPlatformIsRefused(t *testing.T) {
	for _, name := range []string{"macos", "osx", "MacOS", "windows", ""} {
		path := write(t, `{"version":1,"display":1,"displays":{"`+name+`":2},"inputs":[]}`)
		if _, err := Load(path); err == nil {
			t.Errorf("Load accepted displays.%q", name)
		}
	}
}

func TestAValidPlatformIsAccepted(t *testing.T) {
	path := write(t, `{"version":1,"display":1,"displays":{"linux":2,"darwin":3},"inputs":[]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Displays["linux"] != 2 || cfg.Displays["darwin"] != 3 {
		t.Errorf("Displays = %v, want both kept", cfg.Displays)
	}
}

func TestAnImpossiblePlatformNumberIsRefused(t *testing.T) {
	for _, n := range []string{"0", "-1", "1000"} {
		path := write(t, `{"version":1,"display":1,"displays":{"linux":`+n+`},"inputs":[]}`)
		if _, err := Load(path); err == nil {
			t.Errorf("Load accepted displays.linux = %s", n)
		}
	}
}

// A file written before this field existed must still load, and must not gain
// an empty map on the way.
func TestAFileWithNoDisplaysStillWorks(t *testing.T) {
	path := write(t, `{"version":1,"display":2,"inputs":[{"key":"dp","vcp":15}]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisplayNumber() != 2 {
		t.Errorf("DisplayNumber() = %d, want 2", cfg.DisplayNumber())
	}
	if cfg.Displays != nil {
		t.Errorf("Displays = %v, want nil", cfg.Displays)
	}
}

// Round trip: the map survives a save, so a shared file stays shared.
func TestDisplaysRoundTrip(t *testing.T) {
	path := write(t, `{"version":1,"display":1,"displays":{"linux":1,"darwin":4},"inputs":[]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetPath(path)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Displays["darwin"] != 4 {
		t.Errorf("darwin = %d, want 4 after a round trip", again.Displays["darwin"])
	}
}

// One test, both machines. A rule that can only be checked for the platform it
// runs on is a rule nobody can check, so the platform is an argument.
func TestResolveDisplayForBothMachines(t *testing.T) {
	cases := []struct {
		name     string
		fallback int
		per      map[string]int
		linux    int
		darwin   int
	}{
		{
			name:     "both set, as a shared file would be",
			fallback: 1,
			per:      map[string]int{"linux": 1, "darwin": 4},
			linux:    1,
			darwin:   4,
		},
		{
			name:     "neither set, an old file",
			fallback: 2,
			per:      nil,
			linux:    2,
			darwin:   2,
		},
		{
			name:     "only the other one set",
			fallback: 3,
			per:      map[string]int{"darwin": 7},
			linux:    3,
			darwin:   7,
		},
		{
			name:     "an entry that could not be reached is ignored",
			fallback: 5,
			per:      map[string]int{"linux": 0},
			linux:    5,
			darwin:   5,
		},
	}
	for _, c := range cases {
		if got := resolveDisplay(c.fallback, c.per, "linux"); got != c.linux {
			t.Errorf("%s: linux = %d, want %d", c.name, got, c.linux)
		}
		if got := resolveDisplay(c.fallback, c.per, "darwin"); got != c.darwin {
			t.Errorf("%s: darwin = %d, want %d", c.name, got, c.darwin)
		}
	}
}

// The defaults are what run on a machine that has never been configured, and
// they were returned without validation. Every input then carried the empty
// string as its channel, which is neither off nor a channel: on macOS the
// backend refused it, and on Linux it became `change-host ”` — a command that
// runs, fails, and has already moved the monitor by the time it does.
func TestTheDefaultsAreValidatedToo(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "never-written.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.Inputs {
		if string(in.Host) != HostOff {
			t.Errorf("%s has channel %q, want %q", in.Key, in.Host, HostOff)
		}
	}
}

func TestDefaultCarriesOffExplicitly(t *testing.T) {
	for _, in := range Default().Inputs {
		if in.Host == "" {
			t.Errorf("%s has an empty channel, which is not the same as off", in.Key)
		}
	}
}
