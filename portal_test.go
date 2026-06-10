package portal

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestIsWayland(t *testing.T) {
	// Save original environment
	origType := os.Getenv("XDG_SESSION_TYPE")
	origForce := os.Getenv("WAYLAND_PORTAL_FORCE_WAYLAND")
	defer func() {
		os.Setenv("XDG_SESSION_TYPE", origType)
		os.Setenv("WAYLAND_PORTAL_FORCE_WAYLAND", origForce)
	}()

	// Test 1: Native Wayland
	os.Setenv("XDG_SESSION_TYPE", "wayland")
	os.Setenv("WAYLAND_PORTAL_FORCE_WAYLAND", "")
	if !IsWayland() {
		t.Error("Expected IsWayland() to be true when XDG_SESSION_TYPE=wayland")
	}

	// Test 2: Native X11
	os.Setenv("XDG_SESSION_TYPE", "x11")
	if IsWayland() {
		t.Error("Expected IsWayland() to be false when XDG_SESSION_TYPE=x11")
	}

	// Test 3: X11 with Force Flag
	os.Setenv("WAYLAND_PORTAL_FORCE_WAYLAND", "1")
	if !IsWayland() {
		t.Error("Expected IsWayland() to be true when WAYLAND_PORTAL_FORCE_WAYLAND=1")
	}
}

func TestNewScreenCastSession(t *testing.T) {
	// Test empty app name
	_, err := NewScreenCastSession("")
	if err == nil {
		t.Error("Expected error when creating session with empty app name, got nil")
	}

	// Test valid app name
	session, err := NewScreenCastSession("test_app")
	if err != nil {
		t.Fatalf("Failed to create session with valid app name: %v", err)
	}
	defer session.Close()

	if session.appName != "test_app" {
		t.Errorf("Expected appName to be 'test_app', got %s", session.appName)
	}
}

func TestHandshake_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Start the mock portal
	mock, err := StartMockPortal(ctx)
	if err != nil {
		t.Fatalf("Failed to start mock portal: %v", err)
	}
	// Ensure SimulateCancel is false for the success test
	mock.SimulateCancel = false

	// 2. Create the session
	session, err := NewScreenCastSession("test_success")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	// 3. Perform the handshake
	opts := ScreenCastOptions{
		Audio: true, // Request audio so we can test both nodes
	}

	info, err := session.Handshake(ctx, opts)
	if err != nil {
		t.Fatalf("Handshake failed unexpectedly: %v", err)
	}

	// 4. Verify the structured results from the mock
	if info == nil {
		t.Fatal("Expected StreamInfo, got nil")
	}
	if info.VideoNodeID != 42 {
		t.Errorf("Expected VideoNodeID to be 42, got %d", info.VideoNodeID)
	}
	if info.AudioNodeID != 43 {
		t.Errorf("Expected AudioNodeID to be 43, got %d", info.AudioNodeID)
	}
	if info.PipeWireFD <= 0 {
		t.Errorf("Expected a valid PipeWire FD (>0), got %d", info.PipeWireFD)
	}
}

func TestHandshake_UserCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mock, err := StartMockPortal(ctx)
	if err != nil {
		t.Fatalf("Failed to start mock portal: %v", err)
	}

	// Trigger the cancellation simulation
	mock.SimulateCancel = true

	session, err := NewScreenCastSession("test_cancel")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	_, err = session.Handshake(ctx, ScreenCastOptions{})

	// Verify that the exact ErrUserCancelled is returned
	if err == nil {
		t.Fatal("Expected an error from Handshake, got nil")
	}
	if !errors.Is(err, ErrUserCancelled) {
		t.Errorf("Expected error to be ErrUserCancelled, got: %v", err)
	}
}
