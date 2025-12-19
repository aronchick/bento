package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/warpstreamlabs/bento/public/service"
)

// AuthFields returns the config fields for Kubernetes authentication.
// These fields are shared across all Kubernetes components.
func AuthFields() []*service.ConfigField {
	return []*service.ConfigField{
		service.NewBoolField("auto_auth").
			Description("Automatically detect authentication method. Tries in-cluster config first, then falls back to kubeconfig.").
			Default(true),
		service.NewStringField("kubeconfig").
			Description("Path to kubeconfig file. If empty and auto_auth is false, uses default kubeconfig location (~/.kube/config).").
			Default("").
			Optional(),
		service.NewStringField("context").
			Description("Kubernetes context to use from kubeconfig. If empty, uses the current context.").
			Default("").
			Optional(),
		service.NewStringField("api_server").
			Description("Kubernetes API server URL. Only used when providing explicit credentials.").
			Default("").
			Optional().
			Advanced(),
		service.NewStringField("token").
			Description("Bearer token for authentication. Can be a service account token.").
			Default("").
			Secret().
			Optional().
			Advanced(),
		service.NewStringField("token_file").
			Description("Path to file containing bearer token.").
			Default("").
			Optional().
			Advanced(),
		service.NewStringField("ca_file").
			Description("Path to CA certificate file for verifying API server.").
			Default("").
			Optional().
			Advanced(),
		service.NewBoolField("insecure_skip_verify").
			Description("Skip TLS certificate verification. Not recommended for production.").
			Default(false).
			Advanced(),
	}
}

// ClientSet contains both the typed and dynamic Kubernetes clients.
type ClientSet struct {
	Typed   kubernetes.Interface
	Dynamic dynamic.Interface
	Config  *rest.Config
}

// GetClientSet creates Kubernetes clients from parsed configuration.
func GetClientSet(ctx context.Context, conf *service.ParsedConfig) (*ClientSet, error) {
	autoAuth, err := conf.FieldBool("auto_auth")
	if err != nil {
		return nil, fmt.Errorf("failed to parse auto_auth: %w", err)
	}

	var config *rest.Config

	if autoAuth {
		// Try in-cluster first
		config, err = rest.InClusterConfig()
		if err != nil {
			// Fall back to kubeconfig
			config, err = buildKubeconfigClient(conf)
			if err != nil {
				return nil, fmt.Errorf("auto auth failed: not running in cluster and kubeconfig not available: %w", err)
			}
		}
	} else {
		// Check for explicit credentials first
		apiServer, _ := conf.FieldString("api_server")
		if apiServer != "" {
			config, err = buildExplicitClient(conf)
		} else {
			config, err = buildKubeconfigClient(conf)
		}
		if err != nil {
			return nil, err
		}
	}

	// Apply TLS settings
	insecure, _ := conf.FieldBool("insecure_skip_verify")
	if insecure {
		config.Insecure = true
		config.CAFile = ""
		config.CAData = nil
	}

	// Create typed client
	typedClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Create dynamic client for CRD support
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &ClientSet{
		Typed:   typedClient,
		Dynamic: dynamicClient,
		Config:  config,
	}, nil
}

func buildKubeconfigClient(conf *service.ParsedConfig) (*rest.Config, error) {
	kubeconfigPath, _ := conf.FieldString("kubeconfig")
	kubeContext, _ := conf.FieldString("context")

	if kubeconfigPath == "" {
		// Use default kubeconfig location
		if home := os.Getenv("HOME"); home != "" {
			kubeconfigPath = filepath.Join(home, ".kube", "config")
		} else if userprofile := os.Getenv("USERPROFILE"); userprofile != "" {
			// Windows support
			kubeconfigPath = filepath.Join(userprofile, ".kube", "config")
		}
	}

	// Expand ~ in path
	if len(kubeconfigPath) > 0 && kubeconfigPath[0] == '~' {
		if home := os.Getenv("HOME"); home != "" {
			kubeconfigPath = filepath.Join(home, kubeconfigPath[1:])
		}
	}

	loadingRules := &clientcmd.ClientConfigLoadingRules{
		ExplicitPath: kubeconfigPath,
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		configOverrides.CurrentContext = kubeContext
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		configOverrides,
	)

	return clientConfig.ClientConfig()
}

func buildExplicitClient(conf *service.ParsedConfig) (*rest.Config, error) {
	apiServer, _ := conf.FieldString("api_server")
	if apiServer == "" {
		return nil, errors.New("api_server is required for explicit authentication")
	}

	config := &rest.Config{
		Host: apiServer,
	}

	// Token authentication
	token, _ := conf.FieldString("token")
	tokenFile, _ := conf.FieldString("token_file")

	if token != "" {
		config.BearerToken = token
	} else if tokenFile != "" {
		tokenBytes, err := os.ReadFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read token file: %w", err)
		}
		config.BearerToken = string(tokenBytes)
	}

	// CA certificate
	caFile, _ := conf.FieldString("ca_file")
	if caFile != "" {
		config.CAFile = caFile
	}

	return config, nil
}

// InClusterNamespace returns the namespace this pod is running in,
// or "default" if not running in a cluster.
func InClusterNamespace() string {
	// Try to read the namespace from the service account
	nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err == nil {
		return string(nsBytes)
	}
	return "default"
}
