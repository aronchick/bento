package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKubernetesMetricsConfigParse(t *testing.T) {
	spec := kubernetesMetricsInputConfig()

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
			name: "with explicit endpoints",
			config: `
scrape_pods: false
scrape_endpoints:
  - http://prometheus:9090/metrics
  - http://grafana:3000/metrics
`,
			expectError: false,
		},
		{
			name: "full config",
			config: `
namespaces:
  - default
  - monitoring
label_selector: "app=myapp"
scrape_pods: true
scrape_endpoints: []
scrape_annotation: "prometheus.io/scrape"
port_annotation: "prometheus.io/port"
path_annotation: "prometheus.io/path"
scheme_annotation: "prometheus.io/scheme"
scrape_interval: "30s"
scrape_timeout: "15s"
honor_labels: true
default_port: 9090
default_path: "/metrics"
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

func TestMetricParsing(t *testing.T) {
	k := &kubernetesMetricsInput{}
	defaultTS := int64(1234567890)

	tests := []struct {
		name     string
		line     string
		expected *Metric
	}{
		{
			name: "simple metric without labels",
			line: "http_requests_total 12345",
			expected: &Metric{
				Name:      "http_requests_total",
				Labels:    map[string]string{},
				Value:     12345,
				Timestamp: defaultTS,
			},
		},
		{
			name: "metric with labels",
			line: `http_requests_total{method="GET",status="200"} 42`,
			expected: &Metric{
				Name:      "http_requests_total",
				Labels:    map[string]string{"method": "GET", "status": "200"},
				Value:     42,
				Timestamp: defaultTS,
			},
		},
		{
			name: "metric with timestamp",
			line: `process_cpu_seconds_total 0.5 1609459200`,
			expected: &Metric{
				Name:      "process_cpu_seconds_total",
				Labels:    map[string]string{},
				Value:     0.5,
				Timestamp: 1609459200,
			},
		},
		{
			name: "metric with scientific notation",
			line: `memory_bytes 1.5e9`,
			expected: &Metric{
				Name:      "memory_bytes",
				Labels:    map[string]string{},
				Value:     1.5e9,
				Timestamp: defaultTS,
			},
		},
		{
			name:     "comment line",
			line:     "# HELP http_requests_total Total requests",
			expected: nil,
		},
		{
			name:     "empty line",
			line:     "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := k.parseLine(tt.line, defaultTS)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tt.expected.Name, result.Name)
				assert.Equal(t, tt.expected.Labels, result.Labels)
				assert.Equal(t, tt.expected.Value, result.Value)
				assert.Equal(t, tt.expected.Timestamp, result.Timestamp)
			}
		})
	}
}

func TestLabelsToJSON(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name:     "empty labels",
			labels:   map[string]string{},
			expected: "{}",
		},
		{
			name:     "single label",
			labels:   map[string]string{"method": "GET"},
			expected: `{"method":"GET"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := labelsToJSON(tt.labels)
			if len(tt.labels) <= 1 {
				// For empty or single label, we can do exact match
				assert.Equal(t, tt.expected, result)
			} else {
				// For multiple labels, just verify it's valid JSON structure
				assert.Greater(t, len(result), 2)
			}
		})
	}
}
