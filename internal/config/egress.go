package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// parseEgressMappings scans environ for EGRESS_CONNECTION_MAPPING_[n] keys.
// Format: "<listen_port>:<target_host>:<target_port>"
// Duplicate listen ports are rejected. Missing prefix → empty slice, no error.
func parseEgressMappings(environ []string) []EgressMapping {
	var mappings []EgressMapping
	seenPorts := []int{}

	for _, kv := range environ {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if !strings.HasPrefix(parts[0], "EGRESS_CONNECTION_MAPPING_") {
			continue
		}

		fields := strings.SplitN(parts[1], ":", 3)
		if len(fields) != 3 {
			panic(fmt.Sprintf("invalid EGRESS_CONNECTION_MAPPING value %q: expected <port>:<host>:<port>", parts[1]))
		}

		listenPort, err := strconv.Atoi(fields[0])
		if err != nil {
			panic(fmt.Sprintf("invalid listen port in %q: %v", parts[1], err))
		}
		targetPort, err := strconv.Atoi(fields[2])
		if err != nil {
			panic(fmt.Sprintf("invalid target port in %q: %v", parts[1], err))
		}

		if slices.Contains(seenPorts, listenPort) {
			panic(fmt.Sprintf("duplicate egress listen port %d", listenPort))
		}
		seenPorts = append(seenPorts, listenPort)

		mappings = append(mappings, EgressMapping{
			ListenPort: listenPort,
			TargetAddr: fields[1],
			TargetPort: targetPort,
		})
	}
	return mappings
}
