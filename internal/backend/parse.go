package backend

// The output parsers live here rather than beside the backends that call them.
//
// They are ordinary string handling with nothing platform-specific in them, and
// a parser that can only be tested on the platform it runs on is a parser that
// is tested on one machine. Both are checked against captured output from the
// real programs instead.

import (
	"regexp"
	"strconv"
	"strings"
)

// deviceLine is how solaar starts each paired device: two spaces, a number, and
// the name it prints in capitals.
//
// The indentation is what tells this apart from the feature lines further down,
// which look the same but are indented much further. Being wrong here would
// list a HID++ feature as a keyboard.
var deviceLine = regexp.MustCompile(`^ {2,3}(\d+): (.+)$`)

var (
	fieldLine  = regexp.MustCompile(`^\s+([A-Za-z ]+?)\s*:\s*(.+)$`)
	hostInText = regexp.MustCompile(`^\s*Change Host\s*:\s*(\d+):`)
)

// parseSolaarShow turns `solaar show` into devices. It is a function of the
// text so it can be checked against captured output, offline and online alike.
func parseSolaarShow(text string) []Device {
	var out []Device
	var cur *Device
	flush := func() {
		if cur == nil {
			return
		}
		if cur.Name == "" {
			cur.Name = cur.friendly
		}
		out = append(out, *cur)
		cur = nil
	}

	for _, line := range strings.Split(text, "\n") {
		if m := deviceLine.FindStringSubmatch(line); m != nil {
			flush()
			// Online until something says otherwise: a device with a full
			// block is answering, because a device that is not gets the two
			// lines above and no more.
			cur = &Device{Name: m[2], friendly: m[2], Online: true}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.Contains(line, "Device is offline") {
			cur.Online = false
			// Short. The explanation belongs once in the footer of a listing,
			// not repeated after every device.
			cur.Why = "offline"
			continue
		}
		if m := hostInText.FindStringSubmatch(line); m != nil {
			cur.Host = m[1]
			continue
		}
		// The capitalised name on the device line is a display name; the Name
		// field below it is what Solaar matches on. It is the one that has to
		// line up with the settings.
		if m := fieldLine.FindStringSubmatch(line); m != nil {
			switch strings.TrimSpace(m[1]) {
			case "Name":
				cur.Name = strings.TrimSpace(m[2])
			case "Kind":
				cur.Kind = strings.TrimSpace(m[2])
			}
		}
	}
	flush()
	return out
}

// parseMxswitchList turns mxswitch's listing into devices.
//
// Each line is `0x<usage page>:0x<usage>  in=<n>  <name>`. The usage page and
// usage are what say keyboard or mouse: generic desktop, mouse or keyboard.
// Everything else is a vendor collection belonging to a receiver rather than a
// device a person would recognise.
func parseMxswitchList(text string) []Device {
	var out []Device
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		page, usage, ok := parseUsage(fields[0])
		if !ok || page != 0x0001 {
			// Not a generic desktop collection, so not a device.
			continue
		}
		kind := ""
		switch usage {
		case 0x0002:
			kind = "mouse"
		case 0x0006:
			kind = "keyboard"
		default:
			// A generic desktop collection that is neither is a receiver's
			// control interface rather than something to move.
			continue
		}
		// The name is everything after the `in=<n>` field, because it can
		// contain spaces.
		name := ""
		for i, f := range fields {
			if strings.HasPrefix(f, "in=") && i+1 < len(fields) {
				name = strings.Join(fields[i+1:], " ")
				break
			}
		}
		out = append(out, Device{
			Name:   name,
			Kind:   kind,
			Online: true,
		})
	}
	return out
}

// parseUsage reads the `0x0001:0x0006` field.
func parseUsage(field string) (page, usage int, ok bool) {
	pageText, usageText, found := strings.Cut(field, ":")
	if !found {
		return 0, 0, false
	}
	p, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(pageText), "0x"), 16, 32)
	if err != nil {
		return 0, 0, false
	}
	u, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(usageText), "0x"), 16, 32)
	if err != nil {
		return 0, 0, false
	}
	return int(p), int(u), true
}
