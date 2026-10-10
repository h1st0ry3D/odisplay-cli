# odisplay

Switch a monitor's input source, and the Logitech keyboard and mouse that go with it.

One command moves both. The monitor moves first, and the devices only move once it has.

```
odisplay switch dp
```

## Why it is a program and not a panel script

The order is the whole point. A monitor that did not move means you are still
looking at the machine whose keyboard you have in your hand, so the devices stay
put. The devices move only after the monitor has actually switched, and if one
device fails the rest are left alone rather than half-moved.

This logic lived inside the Omarchy panel, where it was one copy that could
drift. A macOS menu bar app would have been a second copy. It is here instead, so
the panel and the app both call the same code and neither can disagree about the
ordering.

## Commands

| Command | What it does |
| --- | --- |
| `odisplay switch <input>` | Move the monitor, then the devices |
| `odisplay switch <input> --dry-run` | Print the commands, run none of them |
| `odisplay plan <input>` | The same dry run, as a name you can bind to a key |
| `odisplay list` | Show the configured inputs |
| `odisplay doctor` | Check the programs this needs are installed |
| `odisplay set name <input> <name>` | Rename a button |
| `odisplay set host <input> <0-3>` | Set the Easy-Switch channel |
| `odisplay set display <n>` | Set the ddcutil display number |
| `odisplay path` | Print the config file's location |

### Exit codes

These are the contract with whatever is showing the panel, and the reason a
caller can tell the two bad outcomes apart:

| Code | Meaning |
| --- | --- |
| 0 | Done |
| 1 | Usage or config problem |
| 2 | The display did not move, so the devices were left alone |
| 3 | The display moved but the devices did not |
| 4 | A program this needs is not installed |

Code 2 is safe. Code 3 means you are looking at another machine without input,
and the channel button under the mouse or keyboard is the way back.

## Try it before you trust it

`--dry-run` builds every command and runs none of them:

```
$ odisplay switch dp --dry-run
Mac Mini, to linux, would run:
/usr/bin/ddcutil setvcp 60 0x0f --display 1
/usr/bin/solaar config 'MX Keys S' change-host 2
/usr/bin/solaar config 'LIFT VERTICAL ERGONOMIC MOUSE' change-host 2
```

Then check the machine is ready, which also runs nothing:

```
$ odisplay doctor
platform: linux
config:   /home/you/.config/odisplay/odisplay.json
  found  /usr/bin/ddcutil
  found  /usr/bin/solaar

displays (the config says 1):
* display 1   card2-DP-6              DELL S2725DC

ready.
```

`doctor` is worth reading before every new machine, because the display number
is not something you can guess. It is `ddcutil`'s own index, read from what
`ddcutil detect` prints. On a laptop the built-in panel is usually in that list
too, but ddcutil gives it no number because it cannot do DDC/CI, so it does not
push your monitor up by one:

```
displays (the config says 1):
  (not addressable) card1-eDP-1    This is a laptop display.  Laptop displays do not support DDC/CI.
* display 1   card2-DP-6

ready.
```

Counting the lines instead of reading the numbers would call that external
monitor "display 2", name a display ddcutil does not have, and send you off to
change a config that was already right.

The number is not stable forever: if a display stops answering over I2C it
drops out of the list and the others are renumbered. Run `doctor` when a switch
stops working.

`doctor` exits non-zero when it cannot verify the display number rather than
saying `ready`, so `odisplay doctor && odisplay switch dp` does the right thing
when the check did not run.

## Install

### Linux

Needs `ddcutil` and `solaar`. Put the binary somewhere on your `PATH`:

```sh
go build -o /usr/local/bin/odisplay ./cmd/odisplay
```

### macOS

Needs [ddcctl](https://github.com/kfix/ddcctl) for the monitor and
[mxswitch](https://github.com/marcocosta97/mxswitch) for the devices.

```sh
brew install ddcctl
git clone https://github.com/marcocosta97/mxswitch
cd mxswitch && make && make install PREFIX="$HOME/.local"
```

`brew` puts ddcctl in `/opt/homebrew/bin` on Apple Silicon and `/usr/local/bin`
on Intel. Both are looked for, so it does not matter which you have.

mxswitch sends the HID++ ChangeHost command directly, so Logi Options+ does not
need to be running. macOS requires **Input Monitoring** for it, which
`make install` offers to grant. Keep `codesign -s -` in the build: macOS
identifies an unsigned binary by path *and* code hash, so an unsigned rebuild
silently loses its permission.

Then check the machine is ready. Neither of these touches a display:

```sh
mxswitch --info
odisplay doctor
```

## How it works

### The ordering

```
1. ddcctl -d <display> -i <vcp>     or   ddcutil setvcp 60 0x?? --display <n>
2. mxswitch <channel>                or   solaar config '<device>' change-host <host>
   mxswitch <channel>                    solaar config '<device>' change-host <host>
```

The monitor is step 1, always. If it fails the run stops there and exits 2. On
Linux the devices get one Solaar call each, because Solaar matches a device name
exactly and a name that does not match is an error rather than a silent no-op.
The mouse is listed last, so the pointer is still here if the keyboard move is
what went wrong.

**macOS does not get that guarantee.** mxswitch takes no device name: it picks the
device itself, preferring a mouse over a keyboard, and moves one per call. So the
mouse goes first there, whatever order the steps are planned in. The number of
calls comes from how many devices are in the config; the names in it are Solaar's
and are unused on macOS.

### One number, two spellings

The input source is stored as a plain decimal MCCS number, because the two
programs disagree about how to write it. `ddcutil` takes hex, `ddcctl` takes
decimal, and they are the same value:

| Input | Stored | ddcutil | ddcctl |
| --- | --- | --- | --- |
| USB-C | 27 | `0x1b` | `27` |
| DisplayPort-1 | 15 | `0x0f` | `15` |
| HDMI-1 | 17 | `0x11` | `17` |

Find yours with `ddcutil detect` and `ddcutil capabilities`, or `ddcctl -m` on
macOS.

### Host channels

`0` means the devices stay where they are. `1`, `2` and `3` are the Logitech
Easy-Switch channels.

Watch out for this: **host 2 on the keyboard and host 2 on the mouse can be
different machines.** The channel names come from whatever paired the devices,
and a keyboard paired with one machine and a mouse paired with another will each
go where they were paired. If they disagree, set them to the same number on a
machine that both are paired with.

## Config

`$XDG_CONFIG_HOME/odisplay/odisplay.json`, which is
`~/.config/odisplay/odisplay.json` unless you set it. Override it with
`ODISPLAY_CONFIG`, which takes either the file or the directory holding it.

```json
{
  "version": 1,
  "display": 1,
  "inputs": [
    { "key": "usbc", "name": "Laptop", "vcp": 27, "host": "0" },
    { "key": "dp",   "name": "Mac Mini", "vcp": 15, "host": "2" },
    { "key": "hdmi", "name": "PC", "vcp": 17, "host": "3" }
  ],
  "devices": ["MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"]
}
```

`devices` is the exact names Solaar prints, keyboard first and mouse last. Get
them from `solaar show`. macOS reads only how many there are, because mxswitch takes no
names.

`display` is ddcutil's own index from `ddcutil detect`, not the one your
compositor uses.

A file that will not parse is refused rather than replaced with the defaults. A
typo should not quietly point the tool at the wrong display number.

## Safety notes

Every command is built as an argument array and run without a shell, so a device
name containing spaces or quotes cannot become a second command. Children run
with a fixed `PATH=/usr/bin:/bin` and an environment of their own, so no setting
in your session can change what a program name resolves to.

Each call has a timeout, because `ddcutil` can hang when an I2C adapter goes
away underneath it and Solaar waits on a device that may be asleep.

## Status

**Linux is tested on real hardware.** **macOS is not.** The macOS code compiles,
vets and has tests, but no Mac has ever run it. Everything said about ddcctl is
checked against its own documentation; everything said about mxswitch is checked
against its README and its macOS source, not against a device.

Two things to expect on first run there:

- ddcctl needs a TCC grant (Accessibility, or Input Monitoring) before it can
  reach a display. Until then it fails and a switch stops at step 1 with the
  devices left alone, which is the safe direction.
- mxswitch sends the mouse before the keyboard, because it picks the device
  itself. The mouse-last ordering this tool is built around does not hold there.

## Development

```sh
go test ./...              # runs on Linux
GOOS=darwin go vet ./...   # checks the macOS half compiles
go test -race ./...        # both streams of a child are written from two goroutines
```

The argument arrays are asserted literally in `internal/backend/*_test.go`. If a
change makes one of them different, that is a change to the hardware contract
and the reason belongs in the commit message.