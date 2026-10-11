package backend

import (
	"strings"
	"testing"
)

// Captured from this machine with both devices paired but not answering. This
// is the shape that matters: it is what a device that has been switched to
// another machine looks like, and it is what someone checking their settings
// will most often see.
func TestParsingSolaarShowWhileOffline(t *testing.T) {
	const real = `rules cannot access modifier keys in Wayland, accessing process only works on GNOME with Solaar Gnome extension installed
solaar version 1.1.20

Bolt Receiver
  Device path  : /dev/hidraw11
  USB id       : 046d:C548
  Has 2 paired device(s) out of a maximum of 6.
  Notifications: (none)
  Device activity counters: 1=36, 2=53

  1: MX KEYS S
     Device is offline.

  2: LIFT
     Device is offline.

`
	got := parseSolaarShow(real)
	if len(got) != 2 {
		t.Fatalf("devices = %d, want 2", len(got))
	}
	for _, d := range got {
		if d.Online {
			t.Errorf("%s is reported online, but solaar said offline", d.Name)
		}
		if d.Why != "offline" {
			t.Errorf("%s why = %q", d.Name, d.Why)
		}
	}
	if got[0].Name != "MX KEYS S" {
		t.Errorf("first = %q", got[0].Name)
	}
	// The receiver's own lines must not become devices: it is paired with two
	// devices, not one.
	if strings.Contains(got[0].Name, "Receiver") {
		t.Error("the receiver was listed as a device")
	}
}

// The online shape, which carries the name Solaar actually matches on. The
// capitalised name above it is a display name and is not what the settings have
// to agree with.
func TestParsingSolaarShowWhileOnline(t *testing.T) {
	const real = `Bolt Receiver
  Device path  : /dev/hidraw2
  USB id       : 046d:C548
  Has 2 paired device(s) out of a maximum of 6.

  1: MX KEYS S
     Device path  : /dev/hidraw2
     WPID         : B378
     Codename     : MX KEYS S
     Kind         : keyboard
     Protocol     : HID++ 4.5
            Name: MX Keys S
            Kind: keyboard
            Friendly Name: MX KEYS S
        10: CHANGE HOST            {1814} V1
            Change Host        : 1:omarchy
        11: HOSTS INFO             {1815} V2
            Host 0 (paired): omarchy
            Host 1 (paired): Heiko's Mini

  2: LIFT
     Device path  : /dev/hidraw3
            Name: LIFT VERTICAL ERGONOMIC MOUSE
            Kind: mouse
        10: CHANGE HOST            {1814} V1
            Change Host        : 3:Heiko's Mini

`
	got := parseSolaarShow(real)
	if len(got) != 2 {
		t.Fatalf("devices = %d, want 2 (a feature line is not a device)", len(got))
	}
	if got[0].Name != "MX Keys S" || got[1].Name != "LIFT VERTICAL ERGONOMIC MOUSE" {
		t.Errorf("names = %q, %q; want the Name field, not the capitalised line",
			got[0].Name, got[1].Name)
	}
	if !got[0].Online || !got[1].Online {
		t.Error("a device with a full block is answering")
	}
	if got[0].Kind != "keyboard" || got[1].Kind != "mouse" {
		t.Errorf("kinds = %q, %q", got[0].Kind, got[1].Kind)
	}
	// The channel is the number before the colon in "Change Host : 1:omarchy".
	if got[0].Host != "1" {
		t.Errorf("keyboard host = %q, want 1", got[0].Host)
	}
	if got[1].Host != "3" {
		t.Errorf("mouse host = %q, want 3", got[1].Host)
	}
}

// Output with no devices must give no devices rather than something invented.
func TestParsingSolaarShowWithNone(t *testing.T) {
	for _, text := range []string{"", "solaar version 1.1.20\n", "No receivers found\n"} {
		if got := parseSolaarShow(text); len(got) != 0 {
			t.Errorf("parseSolaarShow(%q) = %v, want none", text, got)
		}
	}
}

// `mxswitch --list` prints one line per HID collection, including the
// receiver's own. The usage page and usage are what say which are devices: an
// item a person would recognise is a generic desktop mouse or keyboard, and
// everything else belongs to a receiver.
func TestParsingMxswitchList(t *testing.T) {
	const real = `0x0001:0x0006  in=8  MX Keys S
0x0001:0x0002  in=8  LIFT VERTICAL ERGONOMIC MOUSE
0xFF00:0x0001  in=20  USB Receiver
0xFF00:0x0004  in=20  USB Receiver
0x0001:0x0080  in=2  USB Receiver
`
	got := parseMxswitchList(real)
	if len(got) != 2 {
		t.Fatalf("devices = %d, want the keyboard and the mouse only: %+v", len(got), got)
	}
	if got[0].Name != "MX Keys S" || got[0].Kind != "keyboard" {
		t.Errorf("first = %+v, want the keyboard", got[0])
	}
	// The name has spaces in it, so it is everything after the length field.
	if got[1].Name != "LIFT VERTICAL ERGONOMIC MOUSE" || got[1].Kind != "mouse" {
		t.Errorf("second = %+v, want the mouse with its whole name", got[1])
	}
}

// A line this does not understand must be skipped rather than guessed at.
func TestParsingMxswitchListSkipsRubbish(t *testing.T) {
	got := parseMxswitchList("nonsense\n0x0001\n::\n0xZZZZ:0x0002  in=8  x\n")
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}
