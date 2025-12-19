package kubernetes

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jeffail/shutdown"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/warpstreamlabs/bento/public/service"
)

func kubernetesMetricsInputConfig() *service.ConfigSpec {
	return service.NewConfigSpec().
		Stable().
		Categories("Services", "Kubernetes", "Metrics").
		Version("1.0.0").
		Summary("Scrapes Prometheus metrics from Kubernetes pods and emits them as structured JSON.").
		Description(`
This input discovers pods with Prometheus annotations and scrapes their
metrics endpoints at regular intervals. Each metric is emitted as a
structured JSON message, making it easy to process with Bento pipelines.

### Pod Discovery

Pods are discovered based on the standard Prometheus annotations:
- ` + "`prometheus.io/scrape`" + `: Set to "true" to enable scraping
- ` + "`prometheus.io/port`" + `: Port to scrape (default: 9090)
- ` + "`prometheus.io/path`" + `: Path to scrape (default: /metrics)
- ` + "`prometheus.io/scheme`" + `: http or https (default: http)

### Output Format

Each metric is emitted as a structured JSON object:
` + "```json" + `
{
  "name": "http_requests_total",
  "labels": {"method": "GET", "status": "200"},
  "value": 12345,
  "timestamp": 1234567890
}
` + "```" + `

### Use Cases

- Forward metrics to external time-series databases
- Filter and transform metrics before storage
- Aggregate metrics across pods
- Alert on specific metric thresholds
` + MetadataDescription([]string{
			"kubernetes_namespace",
			"kubernetes_pod_name",
			"kubernetes_pod_ip",
			"kubernetes_node_name",
			"kubernetes_metric_name",
			"kubernetes_scrape_url",
		})).
		Fields(AuthFields()...).
		Fields(CommonFields()...).
		Field(service.NewBoolField("scrape_pods").
			Description("Discover and scrape pods with prometheus.io/scrape=true annotation.").
			Default(true)).
		Field(service.NewStringListField("scrape_endpoints").
			Description("Additional explicit endpoints to scrape (e.g., 'http://prometheus:9090/metrics').").
			Default([]any{}).
			Optional()).
		Field(service.NewStringField("scrape_annotation").
			Description("Annotation key to check for scrape enablement.").
			Default("prometheus.io/scrape").
			Advanced()).
		Field(service.NewStringField("port_annotation").
			Description("Annotation key for metrics port.").
			Default("prometheus.io/port").
			Advanced()).
		Field(service.NewStringField("path_annotation").
			Description("Annotation key for metrics path.").
			Default("prometheus.io/path").
			Advanced()).
		Field(service.NewStringField("scheme_annotation").
			Description("Annotation key for metrics scheme (http/https).").
			Default("prometheus.io/scheme").
			Advanced()).
		Field(service.NewStringField("scrape_interval").
			Description("How often to scrape metrics.").
			Default("15s")).
		Field(service.NewStringField("scrape_timeout").
			Description("Timeout for each scrape request.").
			Default("10s")).
		Field(service.NewBoolField("honor_labels").
			Description("If true, keep original labels from metrics. If false, add pod labels as prefixes.").
			Default(true).
			Advanced()).
		Field(service.NewIntField("default_port").
			Description("Default port to scrape if annotation is not present.").
			Default(9090).
			Advanced()).
		Field(service.NewStringField("default_path").
			Description("Default path to scrape if annotation is not present.").
			Default("/metrics").
			Advanced())
}

func init() {
	err := service.RegisterInput(
		"kubernetes_metrics", kubernetesMetricsInputConfig(),
		func(conf *service.ParsedConfig, mgr *service.Resources) (service.Input, error) {
			return newKubernetesMetricsInput(conf, mgr)
		})
	if err != nil {
		panic(err)
	}
}

// Metric represents a parsed Prometheus metric
type Metric struct {
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp,omitempty"`
}

type scrapeTarget struct {
	url       string
	namespace string
	podName   string
	podIP     string
	nodeName  string
}

type kubernetesMetricsInput struct {
	clientSet  *ClientSet
	httpClient *http.Client
	log        *service.Logger

	// Configuration
	namespaces       []string
	labelSelector    string
	fieldSelector    string
	scrapePods       bool
	scrapeEndpoints  []string
	scrapeAnnotation string
	portAnnotation   string
	pathAnnotation   string
	schemeAnnotation string
	scrapeInterval   time.Duration
	scrapeTimeout    time.Duration
	honorLabels      bool
	defaultPort      int
	defaultPath      string

	// State
	mu          sync.Mutex
	targets     map[string]scrapeTarget
	metricsChan chan *service.Message
	shutSig     *shutdown.Signaller
}

func newKubernetesMetricsInput(conf *service.ParsedConfig, mgr *service.Resources) (*kubernetesMetricsInput, error) {
	k := &kubernetesMetricsInput{
		log:         mgr.Logger(),
		targets:     make(map[string]scrapeTarget),
		metricsChan: make(chan *service.Message, 10000),
		shutSig:     shutdown.NewSignaller(),
	}

	var err error

	// Parse namespaces
	if k.namespaces, err = conf.FieldStringList("namespaces"); err != nil {
		return nil, err
	}

	// Parse selectors
	if k.labelSelector, err = conf.FieldString("label_selector"); err != nil {
		return nil, err
	}
	if k.fieldSelector, err = conf.FieldString("field_selector"); err != nil {
		return nil, err
	}

	// Parse scrape configuration
	if k.scrapePods, err = conf.FieldBool("scrape_pods"); err != nil {
		return nil, err
	}
	if k.scrapeEndpoints, err = conf.FieldStringList("scrape_endpoints"); err != nil {
		return nil, err
	}

	// Parse annotation keys
	if k.scrapeAnnotation, err = conf.FieldString("scrape_annotation"); err != nil {
		return nil, err
	}
	if k.portAnnotation, err = conf.FieldString("port_annotation"); err != nil {
		return nil, err
	}
	if k.pathAnnotation, err = conf.FieldString("path_annotation"); err != nil {
		return nil, err
	}
	if k.schemeAnnotation, err = conf.FieldString("scheme_annotation"); err != nil {
		return nil, err
	}

	// Parse intervals
	intervalStr, err := conf.FieldString("scrape_interval")
	if err != nil {
		return nil, err
	}
	if k.scrapeInterval, err = time.ParseDuration(intervalStr); err != nil {
		return nil, fmt.Errorf("failed to parse scrape_interval: %w", err)
	}

	timeoutStr, err := conf.FieldString("scrape_timeout")
	if err != nil {
		return nil, err
	}
	if k.scrapeTimeout, err = time.ParseDuration(timeoutStr); err != nil {
		return nil, fmt.Errorf("failed to parse scrape_timeout: %w", err)
	}

	// Parse other options
	if k.honorLabels, err = conf.FieldBool("honor_labels"); err != nil {
		return nil, err
	}
	if k.defaultPort, err = conf.FieldInt("default_port"); err != nil {
		return nil, err
	}
	if k.defaultPath, err = conf.FieldString("default_path"); err != nil {
		return nil, err
	}

	// Create HTTP client
	k.httpClient = &http.Client{
		Timeout: k.scrapeTimeout,
	}

	// Get Kubernetes client
	if k.clientSet, err = GetClientSet(context.Background(), conf); err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return k, nil
}

func (k *kubernetesMetricsInput) Connect(ctx context.Context) error {
	// Start discovery and scraping
	go k.discoveryLoop()
	go k.scrapeLoop()
	return nil
}

func (k *kubernetesMetricsInput) discoveryLoop() {
	ticker := time.NewTicker(k.scrapeInterval)
	defer ticker.Stop()

	// Initial discovery
	k.discoverTargets()

	for {
		select {
		case <-ticker.C:
			k.discoverTargets()
		case <-k.shutSig.SoftStopChan():
			return
		}
	}
}

func (k *kubernetesMetricsInput) discoverTargets() {
	if !k.scrapePods {
		return
	}

	ctx := context.Background()
	client := k.clientSet.Typed

	namespaces := k.namespaces
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}

	newTargets := make(map[string]scrapeTarget)

	// Add explicit endpoints
	for _, endpoint := range k.scrapeEndpoints {
		newTargets[endpoint] = scrapeTarget{
			url: endpoint,
		}
	}

	// Discover pods
	for _, ns := range namespaces {
		listOpts := metav1.ListOptions{
			LabelSelector: k.labelSelector,
			FieldSelector: k.fieldSelector,
		}

		pods, err := client.CoreV1().Pods(ns).List(ctx, listOpts)
		if err != nil {
			k.log.Errorf("Failed to list pods in namespace %s: %v", ns, err)
			continue
		}

		for _, pod := range pods.Items {
			target := k.podToTarget(&pod)
			if target != nil {
				newTargets[target.url] = *target
			}
		}
	}

	k.mu.Lock()
	k.targets = newTargets
	k.mu.Unlock()
}

func (k *kubernetesMetricsInput) podToTarget(pod *corev1.Pod) *scrapeTarget {
	annotations := pod.Annotations
	if annotations == nil {
		return nil
	}

	// Check if scraping is enabled
	scrape := annotations[k.scrapeAnnotation]
	if scrape != "true" {
		return nil
	}

	// Only scrape running pods with an IP
	if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
		return nil
	}

	// Get port
	port := k.defaultPort
	if portStr := annotations[k.portAnnotation]; portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// Get path
	path := k.defaultPath
	if p := annotations[k.pathAnnotation]; p != "" {
		path = p
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// Get scheme
	scheme := "http"
	if s := annotations[k.schemeAnnotation]; s != "" {
		scheme = s
	}

	url := fmt.Sprintf("%s://%s:%d%s", scheme, pod.Status.PodIP, port, path)

	return &scrapeTarget{
		url:       url,
		namespace: pod.Namespace,
		podName:   pod.Name,
		podIP:     pod.Status.PodIP,
		nodeName:  pod.Spec.NodeName,
	}
}

func (k *kubernetesMetricsInput) scrapeLoop() {
	ticker := time.NewTicker(k.scrapeInterval)
	defer ticker.Stop()

	// Initial scrape
	k.scrapeAll()

	for {
		select {
		case <-ticker.C:
			k.scrapeAll()
		case <-k.shutSig.SoftStopChan():
			return
		}
	}
}

func (k *kubernetesMetricsInput) scrapeAll() {
	k.mu.Lock()
	targets := make([]scrapeTarget, 0, len(k.targets))
	for _, t := range k.targets {
		targets = append(targets, t)
	}
	k.mu.Unlock()

	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(t scrapeTarget) {
			defer wg.Done()
			k.scrapeTarget(t)
		}(target)
	}
	wg.Wait()
}

func (k *kubernetesMetricsInput) scrapeTarget(target scrapeTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), k.scrapeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", target.url, nil)
	if err != nil {
		k.log.Errorf("Failed to create request for %s: %v", target.url, err)
		return
	}

	resp, err := k.httpClient.Do(req)
	if err != nil {
		k.log.Debugf("Failed to scrape %s: %v", target.url, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		k.log.Debugf("Non-200 response from %s: %d", target.url, resp.StatusCode)
		return
	}

	k.parseMetrics(target, resp.Body)
}

// Prometheus metric line regex
var (
	metricLineRegex = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+([0-9.eE+-]+)(?:\s+(\d+))?$`)
	simpleLineRegex = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\s+([0-9.eE+-]+)(?:\s+(\d+))?$`)
	labelRegex      = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="([^"]*)"`)
)

func (k *kubernetesMetricsInput) parseMetrics(target scrapeTarget, body io.Reader) {
	scanner := bufio.NewScanner(body)
	now := time.Now().Unix()

	for scanner.Scan() {
		line := scanner.Text()

		// Skip comments and empty lines
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}

		metric := k.parseLine(line, now)
		if metric == nil {
			continue
		}

		msg := k.metricToMessage(metric, target)
		select {
		case k.metricsChan <- msg:
		case <-k.shutSig.SoftStopChan():
			return
		default:
			// Channel full, drop metric
			k.log.Warn("Metrics channel full, dropping metric")
		}
	}
}

func (k *kubernetesMetricsInput) parseLine(line string, defaultTimestamp int64) *Metric {
	// Try metric with labels first
	if matches := metricLineRegex.FindStringSubmatch(line); matches != nil {
		name := matches[1]
		labelsStr := matches[2]
		valueStr := matches[3]
		timestampStr := matches[4]

		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			return nil
		}

		labels := make(map[string]string)
		for _, labelMatch := range labelRegex.FindAllStringSubmatch(labelsStr, -1) {
			labels[labelMatch[1]] = labelMatch[2]
		}

		timestamp := defaultTimestamp
		if timestampStr != "" {
			if ts, err := strconv.ParseInt(timestampStr, 10, 64); err == nil {
				timestamp = ts
			}
		}

		return &Metric{
			Name:      name,
			Labels:    labels,
			Value:     value,
			Timestamp: timestamp,
		}
	}

	// Try simple metric without labels
	if matches := simpleLineRegex.FindStringSubmatch(line); matches != nil {
		name := matches[1]
		valueStr := matches[2]
		timestampStr := matches[3]

		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			return nil
		}

		timestamp := defaultTimestamp
		if timestampStr != "" {
			if ts, err := strconv.ParseInt(timestampStr, 10, 64); err == nil {
				timestamp = ts
			}
		}

		return &Metric{
			Name:      name,
			Labels:    map[string]string{},
			Value:     value,
			Timestamp: timestamp,
		}
	}

	return nil
}

func (k *kubernetesMetricsInput) metricToMessage(metric *Metric, target scrapeTarget) *service.Message {
	// Serialize metric to JSON
	metricJSON := fmt.Sprintf(
		`{"name":%q,"labels":%s,"value":%v,"timestamp":%d}`,
		metric.Name,
		labelsToJSON(metric.Labels),
		metric.Value,
		metric.Timestamp,
	)

	msg := service.NewMessage([]byte(metricJSON))

	// Add metadata
	msg.MetaSetMut("kubernetes_namespace", target.namespace)
	msg.MetaSetMut("kubernetes_pod_name", target.podName)
	msg.MetaSetMut("kubernetes_pod_ip", target.podIP)
	msg.MetaSetMut("kubernetes_node_name", target.nodeName)
	msg.MetaSetMut("kubernetes_metric_name", metric.Name)
	msg.MetaSetMut("kubernetes_scrape_url", target.url)

	return msg
}

func labelsToJSON(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%q:%q", k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (k *kubernetesMetricsInput) Read(ctx context.Context) (*service.Message, service.AckFunc, error) {
	select {
	case msg := <-k.metricsChan:
		return msg, func(ctx context.Context, err error) error { return nil }, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-k.shutSig.SoftStopChan():
		return nil, nil, service.ErrEndOfInput
	}
}

func (k *kubernetesMetricsInput) Close(ctx context.Context) error {
	k.shutSig.TriggerSoftStop()
	k.shutSig.TriggerHasStopped()
	return nil
}
