package config

import (
	"os"
	"path/filepath"
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

func TestDisplayOutOfRangeFallsBackToOne(t *testing.T) {
	path := write(t, `{"version":1,"display":0}`)
	cfg, _ := Load(path)
	if cfg.Display != 1 {
		t.Errorf("display = %d, want 1", cfg.Display)
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
