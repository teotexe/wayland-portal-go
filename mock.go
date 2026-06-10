/*
Package portal provides a Go library for Linux XDG Desktop Portal ScreenCasting
via D-Bus and PipeWire.

This file (mock.go) contains the MockScreenCast implementation of the D-Bus interface
defined by the XDG Desktop Portal spec (org.freedesktop.portal.ScreenCast). It is
intended to be run in automated unit and integration tests to simulate compositor
and portal interactions without requiring a running display server (like Wayland/X11)
or physical monitors. By listening on the session bus, it responds to the client's
handshake calls and issues D-Bus signals that replicate standard system behaviors,
such as user confirmation or cancellation.
*/
package portal

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

/*
MockScreenCast implements the org.freedesktop.portal.ScreenCast D-Bus
interface for testing purposes. It simulates the portal's behavior without
requiring a running desktop environment, desktop portal service, or Wayland compositor.

This struct is needed to provide tests with a way to simulate interactions with
D-Bus method calls. By exporting this structure, testing frameworks can instantiate
and modify it at runtime (for example, setting the SimulateCancel field to true
to verify that client applications handle user rejection correctly).
*/
type MockScreenCast struct {
	// conn is the session bus connection used by the mock portal to emit
	// response signals to client applications.
	conn *dbus.Conn

	// SimulateCancel, when true, causes every portal step to respond with
	// code 1 (user cancelled) instead of success. This is needed to verify
	// that [ErrUserCancelled] handling is correctly executed and handled by
	// library consumers.
	SimulateCancel bool
}

/*
CreateSession is the mock implementation of the CreateSession D-Bus method.
It is needed to simulate the first step of the XDG Desktop Portal ScreenCast handshake.
When called, it prints a trace log, schedules an asynchronous signal callback on the
provided request path (mimicking the asynchronous nature of desktop portal actions),
and returns the request path back to the client immediately. If SimulateCancel is
true, it will emit a cancellation response code (1) instead of success (0).
*/
func (m *MockScreenCast) CreateSession(options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	reqPath := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/mock_sender/create_token")
	log.Printf("[MockPortal] CreateSession called, replying on %s", reqPath)

	go func() {
		// Sleep momentarily to simulate asynchronous D-Bus roundtrip latency.
		time.Sleep(100 * time.Millisecond)

		respCode := uint32(0)
		if m.SimulateCancel {
			respCode = 1
		}

		results := map[string]dbus.Variant{
			"session_handle": dbus.MakeVariant("/org/freedesktop/portal/desktop/session/mock_session"),
		}
		if err := m.conn.Emit(reqPath, "org.freedesktop.portal.Request.Response", respCode, results); err != nil {
			log.Printf("[MockPortal] Failed to emit CreateSession Response: %v", err)
		}
	}()

	return reqPath, nil
}

/*
SelectSources is the mock implementation of the SelectSources D-Bus method.
It is needed to simulate the second step of the XDG Desktop Portal ScreenCast handshake.
It processes the source selection request (such as screens, windows, audio flags, and cursor modes)
and asynchronously emits the D-Bus Response signal. It checks the SimulateCancel field to
optionally return a user cancellation code (1), allowing unit tests to simulate a cancellation
at this phase of the transaction.
*/
func (m *MockScreenCast) SelectSources(session dbus.ObjectPath, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	reqPath := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/mock_sender/select_token")
	log.Printf("[MockPortal] SelectSources called on %s, replying on %s", session, reqPath)

	go func() {
		// Sleep momentarily to simulate asynchronous D-Bus roundtrip latency.
		time.Sleep(100 * time.Millisecond)

		respCode := uint32(0)
		if m.SimulateCancel {
			respCode = 1
		}

		results := map[string]dbus.Variant{}
		if err := m.conn.Emit(reqPath, "org.freedesktop.portal.Request.Response", respCode, results); err != nil {
			log.Printf("[MockPortal] Failed to emit SelectSources Response: %v", err)
		}
	}()

	return reqPath, nil
}

/*
mockStream represents the D-Bus wire format for a PipeWire stream descriptor.
It is needed because the Start method returns a custom signature `a(ua{sv})`
(array of structs, each containing an unsigned integer and a dictionary of variants).
Declaring this structure explicitly with standard primitive and dbus types allows the
D-Bus marshaller to serialize it correctly into the expected format on the session bus.
*/
type mockStream struct {
	// NodeID is the simulated PipeWire stream node identifier.
	NodeID uint32

	// Options contains metadata options for the stream, such as "source_type".
	Options map[string]dbus.Variant
}

/*
Start is the mock implementation of the Start D-Bus method.
It is needed to simulate the third step of the XDG Desktop Portal ScreenCast handshake,
which corresponds to the user confirming the share options and compositor initiating the stream.
It asynchronously returns a preconfigured list of PipeWire streams: a video stream (node 42) and
an audio stream (node 43). If SimulateCancel is true, it emits response code 1 (cancelled)
instead of returning the active stream payload.
*/
func (m *MockScreenCast) Start(session dbus.ObjectPath, parentWindow string, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	reqPath := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/mock_sender/start_token")
	log.Printf("[MockPortal] Start called on %s, replying on %s", session, reqPath)

	go func() {
		// Sleep momentarily to simulate asynchronous D-Bus roundtrip latency.
		time.Sleep(100 * time.Millisecond)

		respCode := uint32(0)
		if m.SimulateCancel {
			respCode = 1
		}

		streams := []mockStream{
			{
				NodeID: uint32(42),
				Options: map[string]dbus.Variant{
					"source_type": dbus.MakeVariant(uint32(2)), // WINDOW
				},
			},
			{
				NodeID:  uint32(43),
				Options: map[string]dbus.Variant{},
			},
		}
		results := map[string]dbus.Variant{
			"streams": dbus.MakeVariant(streams),
		}
		if err := m.conn.Emit(reqPath, "org.freedesktop.portal.Request.Response", respCode, results); err != nil {
			log.Printf("[MockPortal] Failed to emit Start Response: %v", err)
		}
	}()

	return reqPath, nil
}

/*
OpenPipeWireRemote is the mock implementation of the OpenPipeWireRemote D-Bus method.
It is needed to simulate the final step of the XDG Desktop Portal ScreenCast handshake.
Instead of connecting to a real system PipeWire daemon (which would require a running system service),
it instantiates an standard OS pipe via `os.Pipe()` and returns the read-end file descriptor
as a D-Bus UnixFD. This allows client code to successfully receive a functional file descriptor
and complete its connection routine without raising descriptor-related errors during tests.
*/
func (m *MockScreenCast) OpenPipeWireRemote(session dbus.ObjectPath, options map[string]dbus.Variant) (dbus.UnixFD, *dbus.Error) {
	log.Printf("[MockPortal] OpenPipeWireRemote called on %s", session)
	r, _, err := os.Pipe()
	if err != nil {
		return dbus.UnixFD(0), dbus.NewError("org.freedesktop.portal.Error.Failed", []interface{}{err.Error()})
	}
	return dbus.UnixFD(r.Fd()), nil
}

/*
StartMockPortal registers a MockScreenCast instance on the D-Bus session
bus under the org.freedesktop.portal.Desktop name.

This function is needed to configure and start the mock environment in integration tests.
It performs the connection setup, requests primary ownership of the desktop portal interface name,
exports the MockScreenCast implementation at the required object paths ("/org/freedesktop/portal/desktop"),
and manages the background lifecycle of the connection, closing the socket when the context is cancelled.
*/
func StartMockPortal(ctx context.Context) (*MockScreenCast, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}

	reply, err := conn.RequestName("org.freedesktop.portal.Desktop", dbus.NameFlagReplaceExisting)
	if err != nil {
		return nil, fmt.Errorf("failed to request name: %w", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("name org.freedesktop.portal.Desktop already owned: %v", reply)
	}

	mockSC := &MockScreenCast{conn: conn}
	err = conn.Export(mockSC, "/org/freedesktop/portal/desktop", "org.freedesktop.portal.ScreenCast")
	if err != nil {
		return nil, fmt.Errorf("failed to export screencast portal object: %w", err)
	}

	log.Println("[MockPortal] Mock ScreenCast portal running on session bus...")

	// Shut down and release the D-Bus connection when the parent context is cancelled.
	go func() {
		<-ctx.Done()
		conn.Close()
		log.Println("[MockPortal] Mock ScreenCast portal stopped.")
	}()

	return mockSC, nil
}
