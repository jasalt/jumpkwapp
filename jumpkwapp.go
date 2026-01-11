/*
Program flow:
  1. Parse CLI flags to build window filter criteria and behavior switches.
  2. Fill a KWin JavaScript template (stored in kwin_script_template.js via go:embed) with those settings.
  3. Write the rendered script to a temporary file and load it through KWin’s D-Bus scripting API.
  4. When a launch command is provided, export a small D-Bus listener (ShouldLaunch) that the KWin script calls back into.
  5. Run the KWin script; it activates or cycles matching windows, or signals that no window matched.
  6. Stop the script, launch the fallback command if requested, and clean up temporary resources.

This mirrors the behavior of the original Python version but uses github.com/godbus/dbus/v5 for D-Bus access
and Go’s standard tooling for distribution.
*/
package main

import (
	"bytes"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"github.com/godbus/dbus/v5"
)

//go:embed kwin_script_template.js
var kwinScriptTemplate string

const (
	kwinService        = "org.kde.KWin"
	kwinScriptingPath  = "/Scripting"
	kwinScriptingIface = "org.kde.kwin.Scripting"
	kwinScriptIface    = "org.kde.kwin.Script"
	responseTimeout    = 5 * time.Second
)

var (
	listenerObjectPath = dbus.ObjectPath("/org/jumpkwapp/Listener")
	listenerInterface  = "org.jumpkwapp.Listener"
)

type config struct {
	filterClass    string
	filterAlt      string
	filterRegex    string
	currentDesktop bool
	toggle         bool
	command        string
}

type scriptParams struct {
	ClassName          string
	CaptionPattern     string
	ClassRegex         string
	Toggle             bool
	CurrentDesktopOnly bool
	DBusAddress        string
}

type launchListener struct {
	ch chan bool
}

func (l *launchListener) ShouldLaunch(decision string) *dbus.Error {
	select {
	case l.ch <- strings.EqualFold(decision, "true"):
	default:
	}
	return nil
}

func main() {
	cfg := parseFlags()
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	filterClass := flag.String("filter", "", "filter by window class (exact match)")
	filterClassShort := flag.String("f", "", "filter by window class (exact match)")
	filterAlt := flag.String("filter-alternative", "", "filter by window caption (regex, case-insensitive)")
	filterAltShort := flag.String("fa", "", "filter by window caption (regex, case-insensitive)")
	filterRegex := flag.String("filter-regex", "", "filter by window class using regex")
	filterRegexShort := flag.String("fr", "", "filter by window class using regex")
	currentDesktop := flag.Bool("current-desktop", false, "only consider windows on the current virtual desktop")
	currentDesktopShort := flag.Bool("d", false, "only consider windows on the current virtual desktop")
	toggle := flag.Bool("toggle", false, "toggle minimize when the window is already active")
	toggleShort := flag.Bool("t", false, "toggle minimize when the window is already active")
	command := flag.String("command", "", "command to run when no matching window is found")
	commandShort := flag.String("c", "", "command to run when no matching window is found")

	flag.Parse()

	return config{
		filterClass:    firstNonEmpty(*filterClass, *filterClassShort),
		filterAlt:      firstNonEmpty(*filterAlt, *filterAltShort),
		filterRegex:    firstNonEmpty(*filterRegex, *filterRegexShort),
		currentDesktop: *currentDesktop || *currentDesktopShort,
		toggle:         *toggle || *toggleShort,
		command:        strings.TrimSpace(firstNonEmpty(*command, *commandShort)),
	}
}

func run(cfg config) error {
	if cfg.filterClass == "" && cfg.filterAlt == "" && cfg.filterRegex == "" {
		return errors.New("you need to specify a window filter (-f, -fa, or -fr)")
	}

	conn, err := dbus.SessionBus()
	if err != nil {
		return fmt.Errorf("connect to session bus: %w", err)
	}
	defer conn.Close()

	dbusAddress := ""
	if cfg.command != "" {
		dbusAddress, err = getUniqueName(conn)
		if err != nil {
			return fmt.Errorf("get unique bus name: %w", err)
		}
	}

	script, err := renderScript(scriptParams{
		ClassName:          cfg.filterClass,
		CaptionPattern:     cfg.filterAlt,
		ClassRegex:         cfg.filterRegex,
		Toggle:             cfg.toggle,
		CurrentDesktopOnly: cfg.currentDesktop,
		DBusAddress:        dbusAddress,
	})
	if err != nil {
		return fmt.Errorf("render KWin script: %w", err)
	}

	scriptFile, err := writeTempScript(script)
	if err != nil {
		return err
	}
	defer os.Remove(scriptFile)

	scriptPath, err := loadKWinScript(conn, scriptFile)
	if err != nil {
		return err
	}

	scriptObj := conn.Object(kwinService, scriptPath)
	stopped := false
	defer func() {
		if stopped {
			return
		}
		if cfg.command == "" {
			go func() {
				time.Sleep(150 * time.Millisecond)
				_ = stopScript(scriptObj)
			}()
			return
		}
		_ = stopScript(scriptObj)
	}()

	var listener *launchListener
	if cfg.command != "" {
		listener = &launchListener{ch: make(chan bool, 1)}
		if err := conn.Export(listener, listenerObjectPath, listenerInterface); err != nil {
			return fmt.Errorf("export listener on D-Bus: %w", err)
		}
		defer func() {
			_ = conn.Export(nil, listenerObjectPath, listenerInterface)
		}()
	}

	if err := scriptObj.Call(kwinScriptIface+".run", 0).Err; err != nil {
		return fmt.Errorf("run KWin script: %w", err)
	}

	if cfg.command == "" {
		return nil
	}

	shouldLaunch, err := waitForDecision(listener.ch, responseTimeout)
	if err != nil {
		return fmt.Errorf("wait for KWin response: %w", err)
	}

	if err := stopScript(scriptObj); err != nil {
		return fmt.Errorf("stop KWin script: %w", err)
	}
	stopped = true

	if shouldLaunch {
		if err := launchCommand(cfg.command); err != nil {
			return fmt.Errorf("launch command: %w", err)
		}
	}

	return nil
}

func launchCommand(command string) error {
	if command == "" {
		return nil
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Start()
}

func waitForDecision(ch <-chan bool, timeout time.Duration) (bool, error) {
	select {
	case decision := <-ch:
		return decision, nil
	case <-time.After(timeout):
		return false, errors.New("timeout waiting for response from KWin script")
	}
}

func stopScript(obj dbus.BusObject) error {
	return obj.Call(kwinScriptIface+".stop", 0).Err
}

func writeTempScript(content string) (string, error) {
	f, err := os.CreateTemp("", "jumpkwapp-*.js")
	if err != nil {
		return "", fmt.Errorf("create temp script: %w", err)
	}
	path := f.Name()

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("write temp script: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close temp script: %w", err)
	}
	return path, nil
}

func loadKWinScript(conn *dbus.Conn, scriptFile string) (dbus.ObjectPath, error) {
	scripting := conn.Object(kwinService, dbus.ObjectPath(kwinScriptingPath))
	call := scripting.Call(kwinScriptingIface+".loadScript", 0, scriptFile)
	if call.Err != nil {
		return "", fmt.Errorf("load KWin script: %w", call.Err)
	}

	var scriptID uint32
	if err := call.Store(&scriptID); err != nil {
		return "", fmt.Errorf("parse script ID: %w", err)
	}

	return dbus.ObjectPath(fmt.Sprintf("/Scripting/Script%d", scriptID)), nil
}

func renderScript(params scriptParams) (string, error) {
	tmpl, err := template.New("kwin-script").Parse(kwinScriptTemplate)
	if err != nil {
		return "", err
	}

	data := struct {
		ClassName          string
		CaptionPattern     string
		ClassRegex         string
		Toggle             bool
		CurrentDesktopOnly bool
		DBusAddress        string
	}{
		ClassName:          escapeForJS(params.ClassName),
		CaptionPattern:     escapeForJS(params.CaptionPattern),
		ClassRegex:         escapeForJS(params.ClassRegex),
		Toggle:             params.Toggle,
		CurrentDesktopOnly: params.CurrentDesktopOnly,
		DBusAddress:        escapeForJS(params.DBusAddress),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

var jsReplacer = strings.NewReplacer(
	"\\", "\\\\",
	"'", "\\'",
	"\n", "\\n",
	"\r", "\\r",
	"\t", "\\t",
)

func escapeForJS(value string) string {
	return jsReplacer.Replace(value)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func getUniqueName(conn *dbus.Conn) (string, error) {
	names := conn.Names()
	if len(names) == 0 {
		return "", errors.New("D-Bus connection does not have a unique name yet")
	}
	for _, name := range names {
		if strings.HasPrefix(name, ":") {
			return name, nil
		}
	}
	return names[0], nil
}
