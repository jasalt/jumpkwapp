# jumpkwapp

Run-or-raise utility for KDE Plasma 6 / Wayland, written in Go.

Originally inspired by [ww-run-or-raise](https://github.com/academo/ww-run-raise) and classic X11 [jumpapp](https://github.com/mkropat/jumpapp), this tool raises an existing window that matches your filters (class, regex, caption, desktop) or launches a command when no match is found. D-Bus messaging is handled with [`github.com/godbus/dbus`](https://github.com/godbus/dbus).

Might also work on Plasma 5 / X11 with minimal adjustments.

## Features

- Filter windows by exact class, class regex, or caption (case-insensitive regex).
- Restrict matches to the current virtual desktop.
- Optional toggle mode minimizes a window if it is already active.
- Optional command launches when no matching window exists.
- Automatically embeds and renders the KWin JavaScript activation logic at runtime.

## Requirements

- Go 1.20 or newer.
- KDE Plasma 6 with KWin’s D-Bus scripting interface available.

## Installation

Clone the repository and run/build:

```bash
go mod tidy          # fetch dependencies (godbus/dbus)
go run ./jumpkwapp.go -f firefox -c "firefox" -t --current-desktop  # run without building
go build ./jumpkwapp.go   # build
./jumpkwapp -f firefox -c firefox -t --current-desktop  # run
```

## Usage

```
jumpkwapp [options]

-f,  --filter               Match window class (exact)
-fa, --filter-alternative   Match window caption (regex, case-insensitive)
-fr, --filter-regex         Match window class (regex)
-d,  --current-desktop      Only consider windows on the current desktop
-t,  --toggle               Minimize the window if it is already active
-c,  --command CMD          Launch CMD if no window matches
```

### Examples

Raise an existing LibreWolf window on the current desktop or launch it if missing:

```bash
jumpkwapp -f librewolf -c librewolf --current-desktop
```

Bind the command to a global shortcut via KDE System Settings → Shortcuts.

## Development

- See `DEBUGGING.md` for tips on querying KWin state.
