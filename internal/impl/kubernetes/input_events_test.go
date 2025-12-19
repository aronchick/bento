package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKubernetesEventsConfigParse(t *testing.T) {
	spec := kubernetesEventsInputConfig()

	tests := []struct {
		name        string
		config      string
		expectError bool
	}{
		{
			name:        "minimal config",
			config:      ``,
			expectError: false,
		},
		{
			name: "filter by warning events only",
			config: `
types:
  - Warning
`,
			expectError: false,
		},
		{
			name: "filter by involved kinds",
			config: `
involved_kinds:
  - Pod
  - Node
types:
  - Warning
`,
			expectError: false,
		},
		{
			name: "full config",
			config: `
namespaces:
  - default
  - kube-system
label_selector: ""
field_selector: ""
types:
  - Normal
  - Warning
involved_kinds:
  - Pod
  - Deployment
reasons:
  - Failed
  - FailedScheduling
include_existing: true
max_event_age: "2h"
auto_auth: true
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
