package kubernetes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Jeffail/shutdown"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/warpstreamlabs/bento/public/service"
)

func kubernetesLogsInputConfig() *service.ConfigSpec {
	return service.NewConfigSpec().
		Stable().
		Categories("Services", "Kubernetes").
		Version("1.0.0").
		Summary("Streams logs from Kubernetes pods, similar to `kubectl logs -f`.").
		Description(`
This input streams logs from one or more Kubernetes pods in real-time.
It automatically discovers pods matching your selectors and manages
log streaming across pod restarts and scaling events.

### Selection

Pods are selected using a combination of namespaces, label selectors,
and optional explicit pod names. At minimum, you should specify either
a label_selector or pod_names.

### Container Selection

By default, logs from all containers in matching pods are streamed.
Use container_names to filter to specific containers.
` + MetadataDescription([]string{
			"kubernetes_namespace",
			"kubernetes_pod_name",
			"kubernetes_pod_uid",
			"kubernetes_container_name",
			"kubernetes_node_name",
			"kubernetes_labels_<key>",
		})).
		Fields(AuthFields()...).
		Fields(CommonFields()...).
		Field(service.NewStringListField("pod_names").
			Description("Explicit pod names to stream logs from. If empty, uses label_selector for discovery.").
			Default([]any{}).
			Optional()).
		Field(service.NewStringListField("container_names").
			Description("Container names to stream logs from. If empty, streams all containers.").
			Default([]any{}).
			Optional()).
		Field(service.NewBoolField("follow").
			Description("Continuously stream logs (like kubectl logs -f). If false, fetches existing logs and exits.").
			Default(true)).
		Field(service.NewStringField("since").
			Description("Only return logs newer than this duration (e.g., '5m', '1h'). Ignored if since_time is set.").
			Default("").
			Example("5m").
			Example("1h").
			Example("24h").
			Optional()).
		Field(service.NewIntField("tail_lines").
			Description("Number of recent log lines to fetch initially. Set to -1 for all available logs.").
			Default(100).
			Example(10).
			Example(1000).
			Example(-1)).
		Field(service.NewBoolField("include_previous").
			Description("Include logs from previous container instances (after restarts).").
			Default(false).
			Advanced()).
		Field(service.NewBoolField("timestamps").
			Description("Prepend each log line with its timestamp from Kubernetes.").
			Default(false)).
		Field(service.NewIntField("max_concurrent_pods").
			Description("Maximum number of pods to stream logs from simultaneously.").
			Default(100).
			Advanced()).
		Field(service.NewStringField("reconnect_interval").
			Description("How long to wait before reconnecting after a stream disconnects.").
			Default("5s").
			Advanced()).
		Field(service.NewStringField("discovery_interval").
			Description("How often to check for new pods matching the selector.").
			Default("30s").
			Advanced()).
		LintRule(`
			root = if this.label_selector.or("") == "" && this.pod_names.or([]).length() == 0 {
				"either label_selector or pod_names must be specified"
			}
		`)
}

func init() {
	err := service.RegisterBatchInput(
		"kubernetes_logs", kubernetesLogsInputConfig(),
		func(conf *service.ParsedConfig, mgr *service.Resources) (service.BatchInput, error) {
			return newKubernetesLogsInput(conf, mgr)
		})
	if err != nil {
		panic(err)
	}
}

type podLogStream struct {
	namespace     string
	podName       string
	podUID        string
	containerName string
	nodeName      string
	labels        map[string]string
	cancel        context.CancelFunc
}

type kubernetesLogsInput struct {
	clientSet *ClientSet
	log       *service.Logger

	// Configuration
	namespaces        []string
	labelSelector     string
	fieldSelector     string
	podNames          []string
	containerNames    []string
	follow            bool
	sinceDuration     time.Duration
	tailLines         int64
	includePrevious   bool
	timestamps        bool
	maxConcurrentPods int
	reconnectInterval time.Duration
	discoveryInterval time.Duration

	// State
	mu              sync.Mutex
	streams         map[string]*podLogStream // key: namespace/pod/container
	logChan         chan *service.Message
	shutSig         *shutdown.Signaller
	containerFilter map[string]struct{}
}

func newKubernetesLogsInput(conf *service.ParsedConfig, mgr *service.Resources) (*kubernetesLogsInput, error) {
	k := &kubernetesLogsInput{
		log:     mgr.Logger(),
		streams: make(map[string]*podLogStream),
		logChan: make(chan *service.Message, 1000),
		shutSig: shutdown.NewSignaller(),
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

	// Parse pod and container names
	if k.podNames, err = conf.FieldStringList("pod_names"); err != nil {
		return nil, err
	}
	if k.containerNames, err = conf.FieldStringList("container_names"); err != nil {
		return nil, err
	}

	// Build container filter
	k.containerFilter = make(map[string]struct{})
	for _, c := range k.containerNames {
		k.containerFilter[c] = struct{}{}
	}

	// Parse behavior options
	if k.follow, err = conf.FieldBool("follow"); err != nil {
		return nil, err
	}

	sinceStr, _ := conf.FieldString("since")
	if sinceStr != "" {
		k.sinceDuration, err = time.ParseDuration(sinceStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse since duration: %w", err)
		}
	}

	tailLines, err := conf.FieldInt("tail_lines")
	if err != nil {
		return nil, err
	}
	k.tailLines = int64(tailLines)

	if k.includePrevious, err = conf.FieldBool("include_previous"); err != nil {
		return nil, err
	}
	if k.timestamps, err = conf.FieldBool("timestamps"); err != nil {
		return nil, err
	}

	// Parse performance options
	if k.maxConcurrentPods, err = conf.FieldInt("max_concurrent_pods"); err != nil {
		return nil, err
	}

	reconnectStr, err := conf.FieldString("reconnect_interval")
	if err != nil {
		return nil, err
	}
	if k.reconnectInterval, err = time.ParseDuration(reconnectStr); err != nil {
		return nil, fmt.Errorf("failed to parse reconnect_interval: %w", err)
	}

	discoveryStr, err := conf.FieldString("discovery_interval")
	if err != nil {
		return nil, err
	}
	if k.discoveryInterval, err = time.ParseDuration(discoveryStr); err != nil {
		return nil, fmt.Errorf("failed to parse discovery_interval: %w", err)
	}

	// Get Kubernetes client
	if k.clientSet, err = GetClientSet(context.Background(), conf); err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return k, nil
}

func (k *kubernetesLogsInput) Connect(ctx context.Context) error {
	// Start discovery loop
	go k.discoveryLoop()
	return nil
}

func (k *kubernetesLogsInput) discoveryLoop() {
	ticker := time.NewTicker(k.discoveryInterval)
	defer ticker.Stop()

	// Initial discovery
	k.discoverPods()

	for {
		select {
		case <-ticker.C:
			k.discoverPods()
		case <-k.shutSig.SoftStopChan():
			return
		}
	}
}

func (k *kubernetesLogsInput) discoverPods() {
	ctx := context.Background()
	client := k.clientSet.Typed

	namespaces := k.namespaces
	if len(namespaces) == 0 {
		namespaces = []string{""} // Empty string means all namespaces
	}

	discoveredPods := make(map[string]corev1.Pod)

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
			// Filter by explicit pod names if specified
			if len(k.podNames) > 0 {
				found := false
				for _, name := range k.podNames {
					if pod.Name == name {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}

			// Only stream from running pods
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}

			discoveredPods[fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)] = pod
		}
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	// Start streams for new pods
	for key, pod := range discoveredPods {
		k.startPodStreams(pod)
		_ = key
	}

	// Stop streams for removed pods
	for key, stream := range k.streams {
		podKey := fmt.Sprintf("%s/%s", stream.namespace, stream.podName)
		if _, exists := discoveredPods[podKey]; !exists {
			k.log.Debugf("Pod %s no longer exists, stopping stream", key)
			stream.cancel()
			delete(k.streams, key)
		}
	}
}

func (k *kubernetesLogsInput) startPodStreams(pod corev1.Pod) {
	for _, container := range pod.Spec.Containers {
		// Apply container filter
		if len(k.containerFilter) > 0 {
			if _, ok := k.containerFilter[container.Name]; !ok {
				continue
			}
		}

		streamKey := fmt.Sprintf("%s/%s/%s", pod.Namespace, pod.Name, container.Name)
		if _, exists := k.streams[streamKey]; exists {
			continue // Already streaming
		}

		// Check max concurrent limit
		if len(k.streams) >= k.maxConcurrentPods {
			k.log.Warnf("Max concurrent pods limit (%d) reached, not starting stream for %s",
				k.maxConcurrentPods, streamKey)
			return
		}

		ctx, cancel := context.WithCancel(context.Background())
		stream := &podLogStream{
			namespace:     pod.Namespace,
			podName:       pod.Name,
			podUID:        string(pod.UID),
			containerName: container.Name,
			nodeName:      pod.Spec.NodeName,
			labels:        pod.Labels,
			cancel:        cancel,
		}
		k.streams[streamKey] = stream

		go k.streamContainerLogs(ctx, stream)
	}
}

func (k *kubernetesLogsInput) streamContainerLogs(ctx context.Context, stream *podLogStream) {
	client := k.clientSet.Typed

	for {
		select {
		case <-ctx.Done():
			return
		case <-k.shutSig.SoftStopChan():
			return
		default:
		}

		opts := &corev1.PodLogOptions{
			Container:  stream.containerName,
			Follow:     k.follow,
			Previous:   k.includePrevious,
			Timestamps: k.timestamps,
		}

		if k.tailLines >= 0 {
			opts.TailLines = &k.tailLines
		}

		if k.sinceDuration > 0 {
			sinceSeconds := int64(k.sinceDuration.Seconds())
			opts.SinceSeconds = &sinceSeconds
		}

		req := client.CoreV1().Pods(stream.namespace).GetLogs(stream.podName, opts)
		logStream, err := req.Stream(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			k.log.Errorf("Failed to open log stream for %s/%s/%s: %v",
				stream.namespace, stream.podName, stream.containerName, err)

			// Wait before retrying
			select {
			case <-time.After(k.reconnectInterval):
			case <-ctx.Done():
				return
			case <-k.shutSig.SoftStopChan():
				return
			}
			continue
		}

		k.readLogStream(ctx, stream, logStream)
		logStream.Close()

		if !k.follow {
			// Non-follow mode: exit after reading existing logs
			return
		}

		// Wait before reconnecting
		select {
		case <-time.After(k.reconnectInterval):
		case <-ctx.Done():
			return
		case <-k.shutSig.SoftStopChan():
			return
		}
	}
}

func (k *kubernetesLogsInput) readLogStream(ctx context.Context, stream *podLogStream, logStream io.ReadCloser) {
	scanner := bufio.NewScanner(logStream)
	// Increase buffer size for long log lines
	const maxLogLineSize = 1024 * 1024 // 1MB
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxLogLineSize)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		case <-k.shutSig.SoftStopChan():
			return
		default:
		}

		line := scanner.Text()
		msg := service.NewMessage([]byte(line))

		// Add metadata
		msg.MetaSetMut("kubernetes_namespace", stream.namespace)
		msg.MetaSetMut("kubernetes_pod_name", stream.podName)
		msg.MetaSetMut("kubernetes_pod_uid", stream.podUID)
		msg.MetaSetMut("kubernetes_container_name", stream.containerName)
		msg.MetaSetMut("kubernetes_node_name", stream.nodeName)

		// Add labels as metadata
		for labelKey, labelValue := range stream.labels {
			msg.MetaSetMut("kubernetes_labels_"+labelKey, labelValue)
		}

		select {
		case k.logChan <- msg:
		case <-ctx.Done():
			return
		case <-k.shutSig.SoftStopChan():
			return
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		k.log.Errorf("Error reading log stream for %s/%s/%s: %v",
			stream.namespace, stream.podName, stream.containerName, err)
	}
}

func (k *kubernetesLogsInput) ReadBatch(ctx context.Context) (service.MessageBatch, service.AckFunc, error) {
	// Collect messages into a batch
	batch := make(service.MessageBatch, 0, 100)

	// Wait for at least one message
	select {
	case msg := <-k.logChan:
		batch = append(batch, msg)
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-k.shutSig.SoftStopChan():
		return nil, nil, service.ErrEndOfInput
	}

	// Collect any additional messages that are immediately available
collectLoop:
	for len(batch) < 100 {
		select {
		case msg := <-k.logChan:
			batch = append(batch, msg)
		default:
			break collectLoop
		}
	}

	return batch, func(ctx context.Context, err error) error {
		// Logs don't require acknowledgment - they're ephemeral
		return nil
	}, nil
}

func (k *kubernetesLogsInput) Close(ctx context.Context) error {
	k.shutSig.TriggerSoftStop()

	k.mu.Lock()
	for _, stream := range k.streams {
		stream.cancel()
	}
	k.streams = make(map[string]*podLogStream)
	k.mu.Unlock()

	k.shutSig.TriggerHasStopped()
	return nil
}
