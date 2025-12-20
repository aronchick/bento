package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKubernetesLogsConfigParse(t *testing.T) {
	spec := kubernetesLogsInputConfig()

	tests := []struct {
		name        string
		config      string
		expectError bool
	}{
		{
			name: "minimal config with label selector",
			config: `
label_selector: "app=myapp"
`,
			expectError: false,
		},
		{
			name: "minimal config with pod names",
			config: `
pod_names:
  - my-pod-1
  - my-pod-2
`,
			expectError: false,
		},
		{
			name: "full config",
			config: `
namespaces:
  - default
  - production
label_selector: "app=myapp,env=prod"
pod_names: []
container_names:
  - app
  - sidecar
follow: true
since: "1h"
tail_lines: 500
include_previous: false
timestamps: true
max_concurrent_pods: 50
reconnect_interval: "10s"
discovery_interval: "1m"
auto_auth: true
`,
			expectError: false,
		},
		{
			name: "config with namespaces only (lint rule will catch this separately)",
			config: `
namespaces:
  - default
label_selector: "app=test"
`,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf, err := spec.ParseYAML(tt.config, nil)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, conf)
			}
		})
	}
}

func TestKubernetesLogsConfigSpec(t *testing.T) {
	spec := kubernetesLogsInputConfig()
	require.NotNil(t, spec)
}
