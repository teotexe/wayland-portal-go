// Package pwrouter provides utilities for programmatically routing
// application audio in PipeWire using pw-dump and pw-link.
package pwrouter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Router manages the creation of virtual sinks and the automatic
// linking of application audio ports to those sinks.
type Router struct {
	SinkName string
	ModuleID string
}

// NewRouter creates a new virtual null sink with the specified name.
func NewRouter(sinkName string) (*Router, error) {
	cleanupStaleNullSinks(sinkName)
	cmdSink := exec.Command("pactl", "load-module", "module-null-sink",
		fmt.Sprintf("sink_name=%s", sinkName),
		fmt.Sprintf("sink_properties=device.description=%q", sinkName))

	output, err := cmdSink.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to create virtual null sink: %w", err)
	}

	return &Router{
		SinkName: sinkName,
		ModuleID: strings.TrimSpace(string(output)),
	}, nil
}

// Close unloads the virtual null sink.
func (r *Router) Close() error {
	if r.ModuleID != "" {
		return exec.Command("pactl", "unload-module", r.ModuleID).Run()
	}
	return nil
}

// WatchAndLink runs a blocking loop that monitors PipeWire for an application's
// audio ports and automatically links them to the router's virtual sink.
// Cancel the context to stop watching.
func (r *Router) WatchAndLink(ctx context.Context, appName string) {
	linked := false
	var activePorts []string
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ports, err := getAppAudioPorts(appName)
			if err != nil {
				if linked {
					linked = false
					activePorts = nil
				}
				continue
			}

			if linked && equalSlices(ports, activePorts) {
				continue
			}

			targets, err := getSinkPlaybackPorts(r.SinkName)
			if err != nil {
				continue
			}

			for i, port := range ports {
				target := targets[0]
				if i < len(targets) {
					target = targets[i]
				}
				_ = exec.Command("pw-link", port, target).Run()
			}

			linked = true
			activePorts = ports
		}
	}
}

// -- Internal Helpers (Extracted from share.go) --

func getAppAudioPorts(appName string) ([]string, error) {
	cmd := exec.Command("pw-dump")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var objects []struct {
		ID   uint32 `json:"id"`
		Type string `json:"type"`
		Info *struct {
			Props map[string]interface{} `json:"props"`
		} `json:"info"`
	}

	if err := json.Unmarshal(output, &objects); err != nil {
		return nil, err
	}

	appNameLower := strings.ToLower(appName)
	var appNodeID uint32
	foundNode := false

	for _, obj := range objects {
		if obj.Type != "PipeWire:Interface:Node" || obj.Info == nil || obj.Info.Props == nil {
			continue
		}
		props := obj.Info.Props

		if mediaClass, ok := props["media.class"].(string); !ok || mediaClass != "Stream/Output/Audio" {
			continue
		}

		match := false
		if nameStr, ok := props["application.name"].(string); ok && strings.Contains(strings.ToLower(nameStr), appNameLower) {
			match = true
		} else if binaryStr, ok := props["application.process.binary"].(string); ok && strings.Contains(strings.ToLower(binaryStr), appNameLower) {
			match = true
		}

		if match {
			appNodeID = obj.ID
			foundNode = true
			break
		}
	}

	if !foundNode {
		return nil, fmt.Errorf("app node not found")
	}

	var portIDs []string
	for _, obj := range objects {
		if obj.Type != "PipeWire:Interface:Port" || obj.Info == nil || obj.Info.Props == nil {
			continue
		}
		props := obj.Info.Props

		var portNodeID uint32
		switch v := props["node.id"].(type) {
		case float64:
			portNodeID = uint32(v)
		case int:
			portNodeID = uint32(v)
		case uint32:
			portNodeID = v
		default:
			continue
		}

		if portNodeID == appNodeID {
			if portDir, ok := props["port.direction"].(string); ok && portDir == "out" {
				portIDs = append(portIDs, fmt.Sprintf("%d", obj.ID))
			}
		}
	}

	if len(portIDs) == 0 {
		return nil, fmt.Errorf("no output ports found")
	}
	return portIDs, nil
}

func getSinkPlaybackPorts(sinkName string) ([]string, error) {
	cmd := exec.Command("pw-dump")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var objects []struct {
		ID   uint32 `json:"id"`
		Type string `json:"type"`
		Info *struct {
			Props map[string]interface{} `json:"props"`
		} `json:"info"`
	}
	_ = json.Unmarshal(output, &objects)

	var sinkNodeID uint32
	foundNode := false
	sinkNameLower := strings.ToLower(sinkName)

	for _, obj := range objects {
		if obj.Type == "PipeWire:Interface:Node" && obj.Info != nil && obj.Info.Props != nil {
			props := obj.Info.Props
			if mc, ok := props["media.class"].(string); ok && mc == "Audio/Sink" {
				if nn, ok := props["node.name"].(string); ok && strings.ToLower(nn) == sinkNameLower {
					sinkNodeID = obj.ID
					foundNode = true
					break
				}
			}
		}
	}

	if !foundNode {
		return nil, fmt.Errorf("sink not found")
	}

	var portIDs []string
	for _, obj := range objects {
		if obj.Type == "PipeWire:Interface:Port" && obj.Info != nil && obj.Info.Props != nil {
			props := obj.Info.Props
			var portNodeID uint32
			switch v := props["node.id"].(type) {
			case float64:
				portNodeID = uint32(v)
			}
			if portNodeID == sinkNodeID {
				if pd, ok := props["port.direction"].(string); ok && pd == "in" {
					portIDs = append(portIDs, fmt.Sprintf("%d", obj.ID))
				}
			}
		}
	}
	return portIDs, nil
}

func cleanupStaleNullSinks(sinkName string) {
	cmd := exec.Command("pactl", "list", "short", "modules")
	output, _ := cmd.Output()
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "module-null-sink") && strings.Contains(line, "sink_name="+sinkName) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				_ = exec.Command("pactl", "unload-module", fields[0]).Run()
			}
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
