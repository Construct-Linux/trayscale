// Package autostart turns starting Trayscale at login on and off
// through the XDG desktop portal's Background interface, the way a
// desktop expects an app to ask, rather than writing an autostart entry
// itself: the portal owns ~/.config/autostart/<app id>.desktop and the
// desktop's settings can show and revoke it.
package autostart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest = "org.freedesktop.portal.Desktop"
	portalPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")

	registryRegister  = "org.freedesktop.host.portal.Registry.Register"
	requestBackground = "org.freedesktop.portal.Background.RequestBackground"
	requestIface      = "org.freedesktop.portal.Request"
)

// Command is what the desktop runs at login: the window stays hidden,
// so Trayscale is only in the tray.
var Command = []string{"trayscale", "--hide-window"}

// Enabled reports whether Trayscale starts at login for appID: whether
// the entry the portal writes is there. The portal has no call to ask.
func Enabled(appID string) bool {
	path, err := entryPath(appID)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func entryPath(appID string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", appID+".desktop"), nil
}

// Set asks the portal to start Trayscale at login, or not, and returns
// whether it now does. reason is shown if the portal asks the person.
func Set(ctx context.Context, appID string, enable bool, reason string) (bool, error) {
	// A connection of its own: the registry only takes a connection
	// that has made no portal call yet.
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, fmt.Errorf("connect to the session bus: %w", err)
	}
	defer conn.Close()
	return set(ctx, conn, appID, enable, reason)
}

var tokens atomic.Uint64

func set(ctx context.Context, conn *dbus.Conn, appID string, enable bool, reason string) (bool, error) {
	portal := conn.Object(portalDest, portalPath)

	// An app outside a sandbox is otherwise known by its systemd unit's
	// name, which a launch from a terminal does not give it, and the
	// portal refuses autostart to an app it has no id for. A portal
	// older than the registry fails the call; the unit is then all there
	// is, so the error is not the request's.
	_ = portal.CallWithContext(ctx, registryRegister, 0, appID, map[string]dbus.Variant{}).Err

	token := fmt.Sprintf("trayscale_autostart_%d_%d", os.Getpid(), tokens.Add(1))
	handle := requestPath(conn.Names()[0], token)

	// Subscribe before the call: the response can come before the call
	// returns.
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(handle),
		dbus.WithMatchInterface(requestIface),
		dbus.WithMatchMember("Response"),
	}
	if err := conn.AddMatchSignalContext(ctx, match...); err != nil {
		return false, fmt.Errorf("subscribe to the portal's response: %w", err)
	}
	defer conn.RemoveMatchSignalContext(context.WithoutCancel(ctx), match...)

	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"reason":       dbus.MakeVariant(reason),
		"autostart":    dbus.MakeVariant(enable),
		"commandline":  dbus.MakeVariant(Command),
	}
	var got dbus.ObjectPath
	if err := portal.CallWithContext(ctx, requestBackground, 0, "", options).Store(&got); err != nil {
		return false, fmt.Errorf("request background: %w", err)
	}
	if got != handle {
		return false, fmt.Errorf("request background: the portal answers on %s, not %s", got, handle)
	}

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case sig := <-signals:
			if sig.Path != handle || sig.Name != requestIface+".Response" {
				continue
			}
			return response(sig.Body)
		}
	}
}

// requestPath is the object a portal request answers on, which the
// portal documents as derived from the caller's unique name and token.
func requestPath(sender, token string) dbus.ObjectPath {
	sender = strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
}

var errRefused = errors.New("the portal refused to let Trayscale run in the background")

func response(body []any) (bool, error) {
	if len(body) != 2 {
		return false, fmt.Errorf("portal response: %d values, not 2", len(body))
	}
	code, ok := body[0].(uint32)
	if !ok {
		return false, fmt.Errorf("portal response: code is %T", body[0])
	}
	if code != 0 {
		return false, fmt.Errorf("%w (response %d)", errRefused, code)
	}
	results, _ := body[1].(map[string]dbus.Variant)
	enabled, ok := results["autostart"].Value().(bool)
	if !ok {
		return false, errors.New("portal response: no autostart result")
	}
	return enabled, nil
}
