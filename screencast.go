/*
Package portal provides a Go library for Linux XDG Desktop Portal ScreenCasting
via D-Bus and PipeWire. It simplifies the multi-step handshake required to
obtain a PipeWire file descriptor for screen or window capture on Wayland.

This file (screencast.go) contains the core production implementation of the
screencasting client wrapper. It handles connecting to the user's session bus,
creating request handle tokens, sending method calls to the Desktop Portal D-Bus
object ("org.freedesktop.portal.Desktop"), and setting up signal matchers to wait
for asynchronous response signals from the portal service. It also provides the
data structures representing the configuration options and the output streams
returned by the portal.
*/
package portal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// ErrUserCancelled is returned when the user dismisses or cancels the
// portal's screen-share prompt (XDG Desktop Portal response code 1).
// This sentinel error allows developers to distinguish between technical
// errors (such as D-Bus transport failures) and user-driven rejection of the
// capture request, allowing the application to display a friendly message or
// recover gracefully.
var ErrUserCancelled = errors.New("user cancelled the screen share prompt")

// SourceType describes what kind of content the portal should offer for capture.
// It is implemented as a bitmask (using uint32 representation under the hood)
// which allows the developer to bitwise-OR multiple flags together (for example,
// SourceMonitor | SourceWindow) to indicate that they want to offer multiple
// capture options to the user in the portal selection dialog.
type SourceType uint32

const (
	// SourceMonitor allows selecting entire monitors/screens.
	// When requested, the portal GUI will display a list of connected physical
	// screens or workspaces that the user can choose to share.
	SourceMonitor SourceType = 1

	// SourceWindow allows selecting individual application windows.
	// When requested, the portal GUI will display a list of active windows
	// from running applications that the user can choose to share.
	SourceWindow SourceType = 2

	// SourceVirtual allows selecting virtual streams (available on portal v4+).
	// This is typically used in virtual desktop environments or remote desktop
	// sessions where physical screens and local application windows do not exist.
	SourceVirtual SourceType = 4
)

// CursorMode controls how the portal renders the mouse cursor in the capture.
// This is passed as a D-Bus parameter during the SelectSources step. It is needed
// because different applications have different requirements for the visibility
// and latency of the cursor (for example, video players might want the cursor hidden,
// while screen recording utilities want it embedded or sent as metadata).
type CursorMode uint32

const (
	// CursorHidden hides the cursor from the captured stream.
	// The mouse pointer will not be visible at all in the video frames
	// received by the consumer.
	CursorHidden CursorMode = 1

	// CursorEmbedded embeds the cursor into the stream as part of the frame.
	// The compositor will draw the cursor directly onto the pixel buffer before
	// delivering it. This is highly compatible but means the cursor is baked
	// into the video frames.
	CursorEmbedded CursorMode = 2

	// CursorMetadata provides cursor position as separate PipeWire metadata.
	// The compositor delivers the cursor position, hotspot, and image separate from
	// the video frames. The consumer must composite the cursor locally. This allows
	// zero-latency cursor movement and custom cursor styling.
	CursorMetadata CursorMode = 4
)

/*
ScreenCastOptions configures the behavior of the portal's SelectSources call.
It is needed to allow developers to customize the behavior of the screencast negotiation.
By wrapping these choices into a public struct, we prevent the need for hardcoded
parameters, giving the library consumer control over whether screens, windows, or
both are offered, whether they can select multiple sources, whether audio should
be requested, and how the mouse cursor is rendered in the resulting stream.
*/
type ScreenCastOptions struct {
	// SourceTypes is a bitmask of source types to offer (default: SourceMonitor | SourceWindow).
	// It allows selecting screens, windows, or both.
	SourceTypes SourceType

	// Multiple allows the user to select more than one source (default: false).
	// If set to true, the user can check boxes next to multiple monitors or windows
	// to share them all simultaneously in the same portal session.
	Multiple bool

	// CursorMode controls cursor rendering in the stream (default: CursorEmbedded).
	// It dictates how the cursor image is drawn onto or sent alongside the video frames.
	CursorMode CursorMode

	// Audio requests that application audio is captured alongside the video (default: false).
	// If set to true, the portal will also ask the user for permission to capture audio
	// from the selected source (if the compositor supports it) and return an audio node ID.
	Audio bool
}

/*
defaults is an internal method that fills in zero-valued fields of the
ScreenCastOptions struct with safe, standard values. It is needed to make sure
that if a consumer passes a blank ScreenCastOptions{}, the library still sends
valid options (specifically requesting both monitor/window capture types and an
embedded cursor) rather than sending empty parameters which would cause the
Desktop Portal to reject the request.
*/
func (o *ScreenCastOptions) defaults() {
	if o.SourceTypes == 0 {
		o.SourceTypes = SourceMonitor | SourceWindow
	}
	if o.CursorMode == 0 {
		o.CursorMode = CursorEmbedded
	}
}

/*
StreamInfo holds the result of a successful ScreenCast portal handshake.
It is needed to aggregate the multiple returned values from the multi-step handshake
into a clean, structured type. Instead of returning multiple disparate parameters
representing node IDs and raw file descriptors, this struct bundles them together
under descriptive, exported names. This improves code readability for the consumer
and simplifies future extensions to the returned metadata.
*/
type StreamInfo struct {
	// VideoNodeID is the PipeWire node ID for the video capture stream.
	// This ID is used by the consumer when setting up the PipeWire filter
	// or stream object to select the correct video source.
	VideoNodeID uint32

	// AudioNodeID is the PipeWire node ID for the audio capture stream.
	// It is zero when no audio stream was requested or returned by the portal.
	// When audio is enabled and supported, this ID is passed to PipeWire to
	// receive the captured audio frames.
	AudioNodeID uint32

	// PipeWireFD is the open Unix file descriptor for the PipeWire remote connection.
	// The client must pass this file descriptor to their PipeWire library to initiate
	// the connection to the PipeWire daemon. The caller is responsible for closing
	// this descriptor when the screen-sharing session is finished.
	PipeWireFD int
}

/*
ScreenCastSession manages the D-Bus ScreenCast portal handshake.
It is needed to maintain the state of the connection to the D-Bus session bus
and keep track of the application name used to generate unique D-Bus tokens.
By encapsulating the connection and configuration state in this struct, we can
expose clean methods (like Handshake and Close) without polluting the global package namespace.
*/
type ScreenCastSession struct {
	// conn is the active session bus D-Bus connection.
	conn *dbus.Conn

	// appName is the user-supplied application identifier.
	appName string
}

/*
NewScreenCastSession connects to the session bus and creates a new portal session.
The appName parameter is used as a prefix for D-Bus handle tokens (e.g. "myapp"
produces tokens like "myapp_session_123456"). This name must be non-empty and should
consist of alphanumeric characters and underscores to ensure compatibility with D-Bus
object path naming requirements. It is needed because the portal service uses these
handle tokens to authorize and match requests from our client process.
*/
func NewScreenCastSession(appName string) (*ScreenCastSession, error) {
	if appName == "" {
		return nil, fmt.Errorf("appName must not be empty")
	}

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}
	return &ScreenCastSession{conn: conn, appName: appName}, nil
}

/*
Close terminates the underlying D-Bus connection to the session bus.
This is needed to clean up file descriptors and free system resources. Applications
should typically defer a call to Close immediately after successfully creating a session,
ensuring that even in the event of panic or early exit, the connection is closed.
*/
func (s *ScreenCastSession) Close() error {
	return s.conn.Close()
}

/*
Handshake performs the full XDG Desktop Portal ScreenCast flow:

 1. CreateSession: Establishes a session object path with the portal.
 2. SelectSources: Sends parameters choosing which content and cursor modes to request.
 3. Start: Opens the GUI dialog asking the user to choose a source.
 4. OpenPipeWireRemote: Obtains the PipeWire socket connection file descriptor.

It is needed because the XDG Desktop Portal specification describes a multi-step,
asynchronous state machine to ensure security and user privacy. Handshake abstracts
all the complexity of registering D-Bus signals, matching response paths, generating
unique session tokens, and waiting for asynchronous callbacks into a single, synchronous
blocking call.

It returns a *StreamInfo on success. If the user cancels the portal prompt,
[ErrUserCancelled] is returned. The supplied context can be used to set a
deadline or cancel the handshake.
*/
func (s *ScreenCastSession) Handshake(ctx context.Context, opts ScreenCastOptions) (*StreamInfo, error) {
	opts.defaults()

	// Generate unique tokens for requests and session
	// Using a random number generator prevents handle collisions if multiple
	// sessions are established by the same application or other applications on the bus.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	tokenSuffix := fmt.Sprintf("%d", rng.Intn(1000000))
	sessionToken := s.appName + "_session_" + tokenSuffix
	createToken := s.appName + "_create_" + tokenSuffix
	selectToken := s.appName + "_select_" + tokenSuffix
	startToken := s.appName + "_start_" + tokenSuffix

	bus := s.conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")

	// Set up signal matching for responses.
	// Since XDG Portal responses are sent as D-Bus signals, we must tell the D-Bus
	// daemon to forward signals matching the "org.freedesktop.portal.Request" interface
	// and "Response" member name to our connection.
	err := s.conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
		dbus.WithMatchSender("org.freedesktop.portal.Desktop"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to add match signal: %w", err)
	}

	signals := make(chan *dbus.Signal, 10)
	s.conn.Signal(signals)
	defer s.conn.RemoveSignal(signals)

	// waitResponse is an internal helper that blocks and monitors the signal channel.
	// It filters incoming signals by checking if the signal sender, path, and name match
	// the requested step's path. If a match is found, it extracts the portal's response code.
	// If the code is 1 (cancellation), it returns the ErrUserCancelled sentinel. If it is
	// non-zero, it returns a formatted error containing the failure code.
	waitResponse := func(reqPath dbus.ObjectPath, stepName string) (map[string]dbus.Variant, error) {
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case sig := <-signals:
				if sig.Path == reqPath && sig.Name == "org.freedesktop.portal.Request.Response" {
					if len(sig.Body) < 2 {
						return nil, fmt.Errorf("%s: invalid response payload size", stepName)
					}
					respCode, ok1 := sig.Body[0].(uint32)
					results, ok2 := sig.Body[1].(map[string]dbus.Variant)
					if !ok1 || !ok2 {
						return nil, fmt.Errorf("%s: invalid response payload types", stepName)
					}
					if respCode == 1 {
						return nil, ErrUserCancelled
					}
					if respCode != 0 {
						return nil, fmt.Errorf("%s failed with portal response code %d", stepName, respCode)
					}
					return results, nil
				}
			}
		}
	}

	// ── 1. Create Session ──────────────────────────────────────────────
	// In this step, we request the portal to allocate a new screencasting session.
	// We pass a handle token which dictates the object path where our session will be located.
	log.Println("[Portal] Creating ScreenCast session...")
	createOpts := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(sessionToken),
		"handle_token":         dbus.MakeVariant(createToken),
	}
	var createReqPath dbus.ObjectPath
	err = bus.CallWithContext(ctx, "org.freedesktop.portal.ScreenCast.CreateSession", 0, createOpts).Store(&createReqPath)
	if err != nil {
		return nil, fmt.Errorf("CreateSession call failed: %w", err)
	}

	results, err := waitResponse(createReqPath, "CreateSession")
	if err != nil {
		return nil, err
	}

	sessionHandleStr, ok := results["session_handle"].Value().(string)
	if !ok {
		return nil, fmt.Errorf("CreateSession did not return a valid session_handle")
	}
	sessionHandle := dbus.ObjectPath(sessionHandleStr)
	log.Printf("[Portal] Session created: %s", sessionHandle)

	// ── 2. Select Sources ──────────────────────────────────────────────
	// In this step, we configure the type of sources (monitor/window) and cursor behavior
	// we want to request from the user, along with requesting optional audio streams.
	log.Println("[Portal] Selecting screen source...")
	selectOpts := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(opts.SourceTypes)),
		"multiple":     dbus.MakeVariant(opts.Multiple),
		"cursor_mode":  dbus.MakeVariant(uint32(opts.CursorMode)),
		"handle_token": dbus.MakeVariant(selectToken),
		"audio":        dbus.MakeVariant(opts.Audio),
	}
	var selectReqPath dbus.ObjectPath
	err = bus.CallWithContext(ctx, "org.freedesktop.portal.ScreenCast.SelectSources", 0, sessionHandle, selectOpts).Store(&selectReqPath)
	if err != nil {
		return nil, fmt.Errorf("SelectSources call failed: %w", err)
	}

	_, err = waitResponse(selectReqPath, "SelectSources")
	if err != nil {
		return nil, err
	}
	log.Println("[Portal] Screen source selected.")

	// ── 3. Start Session ───────────────────────────────────────────────
	// This call actually triggers the system's screen-sharing selection UI.
	// It blocks waiting for the user to make a choice (or cancel). Once accepted,
	// the response returns a list of active PipeWire streams.
	log.Println("[Portal] Starting ScreenCast session...")
	startOpts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(startToken),
	}
	var startReqPath dbus.ObjectPath
	err = bus.CallWithContext(ctx, "org.freedesktop.portal.ScreenCast.Start", 0, sessionHandle, "", startOpts).Store(&startReqPath)
	if err != nil {
		return nil, fmt.Errorf("Start call failed: %w", err)
	}

	results, err = waitResponse(startReqPath, "Start")
	if err != nil {
		return nil, err
	}

	streamsVal, ok := results["streams"]
	if !ok {
		return nil, fmt.Errorf("Start response did not contain streams")
	}

	// The D-Bus signature is a(ua{sv}): a slice of structs (uint32, map[string]variant).
	// We decode the stream details to find the respective node IDs for video and audio.
	var streams []struct {
		NodeID  uint32
		Options map[string]dbus.Variant
	}
	err = dbus.Store([]interface{}{streamsVal.Value()}, &streams)
	if err != nil {
		return nil, fmt.Errorf("failed to decode streams: %w", err)
	}

	if len(streams) == 0 {
		return nil, fmt.Errorf("no screencast streams returned")
	}

	info := &StreamInfo{}
	if len(streams) == 1 {
		info.VideoNodeID = streams[0].NodeID
		log.Printf("[Portal] 1 stream returned. Video PipeWire Node ID: %d", info.VideoNodeID)
	} else {
		// Differentiate video from audio using the "source_type" key in Options.
		// Video streams typically carry a source_type (monitor or window),
		// while audio streams do not.
		for _, stream := range streams {
			if _, hasSourceType := stream.Options["source_type"]; hasSourceType {
				info.VideoNodeID = stream.NodeID
			} else {
				info.AudioNodeID = stream.NodeID
			}
		}
		// Fallback: if we couldn't differentiate, assume first=video, second=audio.
		if info.VideoNodeID == 0 {
			info.VideoNodeID = streams[0].NodeID
			if len(streams) > 1 {
				info.AudioNodeID = streams[1].NodeID
			}
		}
		log.Printf("[Portal] %d streams returned. Video Node: %d, Audio Node: %d",
			len(streams), info.VideoNodeID, info.AudioNodeID)
	}

	// ── 4. Open PipeWire Remote ────────────────────────────────────────
	// Finally, we call OpenPipeWireRemote to obtain the file descriptor.
	// This file descriptor serves as the main portal socket connecting us
	// directly to the PipeWire server.
	log.Println("[Portal] Opening PipeWire remote...")
	openOpts := map[string]dbus.Variant{}
	var fd dbus.UnixFD
	err = bus.CallWithContext(ctx, "org.freedesktop.portal.ScreenCast.OpenPipeWireRemote", 0, sessionHandle, openOpts).Store(&fd)
	if err != nil {
		return nil, fmt.Errorf("OpenPipeWireRemote call failed: %w", err)
	}

	info.PipeWireFD = int(fd)
	log.Printf("[Portal] PipeWire remote opened. File Descriptor: %d", info.PipeWireFD)
	return info, nil
}

/*
IsWayland reports whether the current environment is running under Wayland.
It is needed because applications targeting Wayland compositors typically want
to switch behavior dynamically if they are running on Legacy X11 or a different OS
altogether. It checks the XDG_SESSION_TYPE environment variable and supports a
WAYLAND_PORTAL_FORCE_WAYLAND override environment variable to simplify unit/integration
testing on headless environments where D-Bus mock loops are running.
*/
func IsWayland() bool {
	return strings.ToLower(os.Getenv("XDG_SESSION_TYPE")) == "wayland" ||
		os.Getenv("WAYLAND_PORTAL_FORCE_WAYLAND") == "1"
}
