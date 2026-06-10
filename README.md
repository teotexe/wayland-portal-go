# wayland-portal-go

A minimal library that simplifies screen and window capture on Wayland 
via the [XDG Desktop Portal](https://flatpak.github.io/xdg-desktop-portal/) ScreenCast interface and [PipeWire](https://pipewire.org/).

On modern Linux running Wayland, applications cannot directly grab the screen. 
Instead they must negotiate through a D-Bus portal that asks the user for 
consent and returns a PipeWire file descriptor. 
This micro-library wraps that entire multi-step handshake in a single function call.

It includes a standalone `pwrouter` utility to programmatically capture audio from specific applications (bypassing buggy desktop environments).

> [!NOTE]
> The code was generated and refined with the assistance of Claude Opus 4.6

## Features

* **Single-call handshake** — `CreateSession → SelectSources → Start → OpenPipeWireRemote` in one `Handshake()` call.
* **Structured results** — returns a `StreamInfo` struct with video/audio PipeWire node IDs and the file descriptor.
* **Configurable** — choose source types (monitors, windows, virtual), cursor mode, multi-select, and audio capture via `ScreenCastOptions`.
* **App Audio Routing** — includes a standalone `pwrouter` utility to programmatically capture audio from specific applications (bypassing buggy desktop environments).

## Installation

```bash
go get github.com/teotexe/wayland-portal-go

```

## Quick Start (Screen & Video)

```go
/*
 * This script initializes a simple session returning:
 * type StreamInfo struct {
 * 		VideoNodeID uint32 
 * 		AudioNodeID uint32
 * 		PipeWireFD int
 * } 
 */
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	portal "github.com/teotexe/wayland-portal-go"
)

func main() {
	// Optional: check if the session is Wayland at all.
	if !portal.IsWayland() {
		log.Fatal("This application requires a Wayland session.")
	}

	// Create a session. The app name prefixes D-Bus tokens for uniqueness.
	session, err := portal.NewScreenCastSession("myapp")
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Configure what to capture.
	opts := portal.ScreenCastOptions{
		SourceTypes: portal.SourceMonitor | portal.SourceWindow,
		CursorMode:  portal.CursorEmbedded,
		Audio:       true, // Request audio via the portal
	}

	// Run the portal handshake. This opens the system share-picker dialog.
	info, err := session.Handshake(ctx, opts)
	if err != nil {
		if errors.Is(err, portal.ErrUserCancelled) {
			fmt.Println("User cancelled the screen share prompt.")
			return
		}
		log.Fatalf("Handshake failed: %v", err)
	}

	fmt.Printf("Video PipeWire Node ID : %d\n", info.VideoNodeID)
	fmt.Printf("Audio PipeWire Node ID : %d\n", info.AudioNodeID) // May be 0 if audio was denied
	fmt.Printf("PipeWire File Descriptor: %d\n", info.PipeWireFD)

}

```

## Advanced Application Audio Routing (`pwrouter`)

Because some Linux desktop environments have buggy implementations of portal-level audio capture, 
this library ships with a `pwrouter` sub-package. It allows you to programmatically find a specific 
application's audio output using `pw-dump` and automatically link it to a virtual null sink using `pw-link`.

```go
/*
 * This script creates an audio sink and links it to Firefox to record its audio.
 */
package main

import (
	"context"
	"log"
	"time"

	"github.com/teotexe/wayland-portal-go/pwrouter"
)

func main() {
	// 1. Create a virtual null sink to capture audio.
	// This creates a device named "MyCaptureSink" in PipeWire/PulseAudio.
	router, err := pwrouter.NewRouter("MyCaptureSink")
	if err != nil {
		log.Fatalf("Failed to create router: %v", err)
	}
	defer router.Close() // Cleans up the virtual sink on exit

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Println("Monitoring for Firefox audio...")

	// 2. Start watching and linking the application's audio in the background.
	// This will dynamically re-link the audio even if the app restarts or mutes.
	go router.WatchAndLink(ctx, "Firefox")

	// 3. Your media pipeline can now record from this sink.
	// E.g., in GStreamer: pulsesrc device=MyCaptureSink.monitor
	
	time.Sleep(1 * time.Hour)
}

```

## Configuration Reference

### `ScreenCastOptions`

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `SourceTypes` | `SourceType` | `SourceMonitor | SourceWindow` | Bitmask of content types to offer (monitors, windows, virtual). |
| `Multiple` | `bool` | `false` | Allow the user to select more than one source. |
| `CursorMode` | `CursorMode` | `CursorEmbedded` | How the mouse cursor appears in the captured stream. |
| `Audio` | `bool` | `false` | Request application/system audio alongside video capture. |

### Source Types

| Constant | Value | Description |
| --- | --- | --- |
| `SourceMonitor` | 1 | Entire monitor / screen. |
| `SourceWindow` | 2 | Individual application window. |
| `SourceVirtual` | 4 | Virtual stream (portal v4+). |

### Cursor Modes

| Constant | Value | Description |
| --- | --- | --- |
| `CursorHidden` | 1 | Cursor is not included in the stream. |
| `CursorEmbedded` | 2 | Cursor is rendered into the video frames. |
| `CursorMetadata` | 4 | Cursor position is sent as PipeWire metadata. |

## Testing with the Mock Portal

The library ships a `MockScreenCast` that can stand in for the real XDG Desktop Portal on the D-Bus session bus. Use it in your integration tests:

```go
package myapp_test

import (
	"context"
	"errors"
	"testing"

	portal "github.com/teotexe/wayland-portal-go"
)

func TestScreenCast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the mock portal on the session bus.
	mock, err := portal.StartMockPortal(ctx)
	if err != nil {
		t.Fatalf("StartMockPortal: %v", err)
	}

	// Create a session using the mock.
	session, err := portal.NewScreenCastSession("testapp")
	if err != nil {
		t.Fatalf("NewScreenCastSession: %v", err)
	}
	defer session.Close()

	info, err := session.Handshake(ctx, portal.ScreenCastOptions{Audio: true})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	if info.VideoNodeID != 42 {
		t.Errorf("expected video node 42, got %d", info.VideoNodeID)
	}
	if info.AudioNodeID != 43 {
		t.Errorf("expected audio node 43, got %d", info.AudioNodeID)
	}
	if info.PipeWireFD < 0 {
		t.Errorf("expected valid FD, got %d", info.PipeWireFD)
	}

	// Test user cancellation.
	mock.SimulateCancel = true
	_, err = session.Handshake(ctx, portal.ScreenCastOptions{})
	if !errors.Is(err, portal.ErrUserCancelled) {
		t.Errorf("expected ErrUserCancelled, got %v", err)
	}
}

```

### How the Mock Works

* `StartMockPortal(ctx)` registers a fake `org.freedesktop.portal.ScreenCast` service on the session bus.
* It returns a `*MockScreenCast` whose `SimulateCancel` field you can toggle at runtime.
* By default it returns success with video node ID **42** and audio node ID **43**.
* Setting `SimulateCancel = true` makes all steps respond with code 1, causing `Handshake` to return `ErrUserCancelled`.
* The mock uses `os.Pipe()` to provide a valid file descriptor for `OpenPipeWireRemote`.

## Error Handling

| Error | When |
| --- | --- |
| `ErrUserCancelled` | The user dismissed the portal share-picker (response code 1). |
| `context.Canceled` | The context was cancelled before the handshake completed. |
| `context.DeadlineExceeded` | The context deadline was exceeded. |
| Other `error` | D-Bus communication failures or unexpected portal responses. |
