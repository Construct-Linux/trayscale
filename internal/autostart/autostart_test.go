package autostart

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

const appID = "dev.deedles.Trayscale"

// fakePortal answers as xdg-desktop-portal does, recording what it was
// asked.
type fakePortal struct {
	conn       *dbus.Conn
	code       uint32
	registered []string
	options    map[string]dbus.Variant
}

func (p *fakePortal) Register(sender dbus.Sender, id string, _ map[string]dbus.Variant) *dbus.Error {
	p.registered = append(p.registered, id)
	return nil
}

func (p *fakePortal) RequestBackground(sender dbus.Sender, _ string, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	p.options = options
	handle := requestPath(string(sender), options["handle_token"].Value().(string))
	results := map[string]dbus.Variant{
		"background": dbus.MakeVariant(p.code == 0),
		"autostart":  dbus.MakeVariant(p.code == 0 && options["autostart"].Value().(bool)),
	}
	go p.conn.Emit(handle, requestIface+".Response", p.code, results)
	return handle, nil
}

// bus starts a private message bus and the fake portal on it, with the
// registry only if registry is true, and returns a client connection.
func bus(t *testing.T, p *fakePortal, registry bool) *dbus.Conn {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon")
	}
	addr := "unix:path=" + filepath.Join(t.TempDir(), "bus")
	cmd := exec.Command(daemon, "--session", "--nofork", "--print-address=1", "--address="+addr)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	_, err = bufio.NewReader(out).ReadString('\n')
	require.NoError(t, err)

	connect := func() *dbus.Conn {
		c, err := dbus.Connect(addr)
		require.NoError(t, err)
		t.Cleanup(func() { c.Close() })
		return c
	}
	p.conn = connect()
	require.NoError(t, p.conn.ExportMethodTable(map[string]any{"RequestBackground": p.RequestBackground}, portalPath, "org.freedesktop.portal.Background"))
	if registry {
		require.NoError(t, p.conn.ExportMethodTable(map[string]any{"Register": p.Register}, portalPath, "org.freedesktop.host.portal.Registry"))
	}
	reply, err := p.conn.RequestName(portalDest, dbus.NameFlagDoNotQueue)
	require.NoError(t, err)
	require.Equal(t, dbus.RequestNameReplyPrimaryOwner, reply)
	return connect()
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestSetAsksThePortalAsTheApp(t *testing.T) {
	p := &fakePortal{}
	conn := bus(t, p, true)

	on, err := set(ctx(t), conn, appID, true, "why")
	require.NoError(t, err)
	require.True(t, on)
	require.Equal(t, []string{appID}, p.registered)
	require.Equal(t, true, p.options["autostart"].Value())
	require.Equal(t, Command, p.options["commandline"].Value())
	require.Equal(t, "why", p.options["reason"].Value())
}

func TestSetTurnsItOff(t *testing.T) {
	p := &fakePortal{}
	on, err := set(ctx(t), bus(t, p, true), appID, false, "")
	require.NoError(t, err)
	require.False(t, on)
	require.Equal(t, false, p.options["autostart"].Value())
}

// A portal older than the registry still knows an app by its unit.
func TestSetWithoutTheRegistry(t *testing.T) {
	p := &fakePortal{}
	on, err := set(ctx(t), bus(t, p, false), appID, true, "")
	require.NoError(t, err)
	require.True(t, on)
}

func TestSetRefused(t *testing.T) {
	p := &fakePortal{code: 1}
	_, err := set(ctx(t), bus(t, p, true), appID, true, "")
	require.ErrorIs(t, err, errRefused)
}

func TestEnabledIsThePortalsEntry(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	require.False(t, Enabled(appID))

	require.NoError(t, os.MkdirAll(filepath.Join(config, "autostart"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "autostart", appID+".desktop"), []byte("[Desktop Entry]\n"), 0o644))
	require.True(t, Enabled(appID))
}

// The fake portal derives the path the same way, so this pins it to the
// portal's documented form.
func TestRequestPath(t *testing.T) {
	require.Equal(t, dbus.ObjectPath("/org/freedesktop/portal/desktop/request/1_42/tok"), requestPath(":1.42", "tok"))
}
