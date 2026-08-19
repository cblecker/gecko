package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/aggregated"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"golang.org/x/net/http2"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/server/healthz"
)

// Server represents the API server with both private and public endpoints.
type Server struct {
	publicRouter     chi.Router
	publicServer     *http.Server
	aggregatedServer *aggregated.AggregatedServer
	stopCh           chan struct{}
	stopOnce         sync.Once
	options          Options
	logger           logr.Logger
}

// PrivateAPIOptions holds configuration for the private API server.
// The private API always uses GenericAPIServer (aggregated mode).
type PrivateAPIOptions struct {
	Port      int
	Resources []ResourceInfo
	Scheme    *runtime.Scheme
	Prefix    string // Optional: prefix for private labels/annotations/conditions filtered during conversion (defaults to conversion.DefaultPrivatePrefix)
	TLSCertFile              string // TLS certificate file (auto-generated if empty)
	TLSKeyFile               string // TLS private key file (auto-generated if empty)
	AuthenticationKubeconfig string // Kubeconfig for delegated authn (in-cluster if empty)
	AuthorizationKubeconfig  string // Kubeconfig for delegated authz (in-cluster if empty)
	DisableAuth              bool   // Disable authn/authz (for testing/local dev)
}

// PublicAPIOptions holds configuration for the public API server.
type PublicAPIOptions struct {
	Enable     bool
	Port       int
	Resources  []ResourceInfo
	Scheme     *runtime.Scheme
	Middleware []func(http.Handler) http.Handler
}

// Options holds server configuration.
type Options struct {
	Address        string
	CORSOrigins    []string
	Private        PrivateAPIOptions
	Public         PublicAPIOptions
	StorageFactory StorageFactory
	Logger         logr.Logger    // Optional: logger for server operations (defaults to discard logger)
}

// New creates a new API server with the given options.
func New(opts Options) (*Server, error) {
	logger := opts.Logger
	if logger.GetSink() == nil {
		logger = logr.Discard()
	}

	if opts.Private.Scheme == nil {
		return nil, fmt.Errorf("private scheme is required")
	}
	if len(opts.Private.Resources) == 0 {
		return nil, fmt.Errorf("at least one private resource is required")
	}
	if opts.StorageFactory == nil {
		return nil, fmt.Errorf("storage factory is required")
	}

	server := &Server{
		options: opts,
		logger:  logger,
		stopCh:  make(chan struct{}),
	}

	// Memoize store instances so the aggregated server and the
	// privateRegistry used by the public API conversion layer
	// operate on the same data.
	var storesMu sync.Mutex
	stores := make(map[string]storage.ResourceStore)
	sharedFactory := func(resourceType string, s *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		storesMu.Lock()
		defer storesMu.Unlock()
		key := resourceType + "/" + gvk.Group + "/" + gvk.Kind
		if st, ok := stores[key]; ok {
			return st, nil
		}
		st, err := opts.StorageFactory(resourceType, s, gvk)
		if err != nil {
			return nil, err
		}
		stores[key] = st
		return st, nil
	}

	// Health checker uses the memoized sharedFactory so probes reuse the
	// already-running store/broadcaster instead of creating a new one on
	// every Check() call. The raw opts.StorageFactory creates backends
	// (e.g. the Spanner store) that spawn long-lived background goroutines
	// (change-stream partition readers) with no way to close them, so using
	// it directly here leaked one broadcaster per liveness/readiness probe
	// and eventually exhausted Spanner's per-partition concurrent query
	// limit.
	first := opts.Private.Resources[0]
	healthCheckers := []healthz.HealthChecker{
		aggregated.NewStorageHealthChecker(sharedFactory, opts.Private.Scheme, GroupKindResourceType(first.GVK.GroupKind()), first.GVK),
	}

	aggCfg := aggregated.Config{
		Scheme:                   opts.Private.Scheme,
		Resources:                opts.Private.Resources,
		StorageFactory:           sharedFactory,
		BindAddress:              opts.Address,
		BindPort:                 opts.Private.Port,
		CertFile:                 opts.Private.TLSCertFile,
		KeyFile:                  opts.Private.TLSKeyFile,
		AuthenticationKubeconfig: opts.Private.AuthenticationKubeconfig,
		AuthorizationKubeconfig:  opts.Private.AuthorizationKubeconfig,
		DisableAuth:              opts.Private.DisableAuth,
		HealthCheckers:           healthCheckers,
		Logger:                   logger,
	}

	completedCfg, err := aggCfg.Complete()
	if err != nil {
		return nil, fmt.Errorf("failed to complete aggregated config: %w", err)
	}

	// Use the resolved bind address (Complete may override to 127.0.0.1 for DisableAuth).
	bindAddress := completedCfg.BindAddress

	aggServer, err := aggregated.New(completedCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create aggregated server: %w", err)
	}

	server.aggregatedServer = aggServer

	if opts.Public.Enable {
		if opts.Public.Scheme == nil {
			return nil, fmt.Errorf("public scheme is required when Public.Enable is true")
		}
		if len(opts.Public.Resources) == 0 {
			return nil, fmt.Errorf("public resources are required when Public.Enable is true")
		}

		// Build privateRegistry for public API conversion.
		// Uses sharedFactory so conversion handlers access the same stores
		// as the aggregated server.
		sharedRegistryOpts := []RegistryOption{WithStorageFactory(sharedFactory), WithLogger(logger)}
		privateRegistry := NewResourceRegistry(opts.Private.Scheme, sharedRegistryOpts...)
		for _, res := range opts.Private.Resources {
			if err := privateRegistry.Register(res); err != nil {
				return nil, fmt.Errorf("failed to register private resource %s: %w", res.Plural, err)
			}
		}

		publicRegistry := NewResourceRegistry(opts.Public.Scheme, WithStorageFactory(sharedFactory), WithLogger(logger))
		for _, res := range opts.Public.Resources {
			if err := publicRegistry.Register(res); err != nil {
				return nil, fmt.Errorf("failed to register public resource %s: %w", res.Plural, err)
			}
		}

		// Build health checker for the public API using the same storage probe as the private API.
		publicHealthCheck := func() error {
			return healthCheckers[0].Check(nil)
		}

		converter := conversion.NewConverter(opts.Public.Scheme, opts.Private.Scheme, opts.Private.Prefix)
		publicRouter, err := setupConvertingRouter(publicRegistry, privateRegistry, converter, opts.Private.Scheme, opts.CORSOrigins, opts.Public.Middleware, publicHealthCheck)
		if err != nil {
			return nil, fmt.Errorf("failed to setup public router: %w", err)
		}

		publicServer := &http.Server{
			Addr:              fmt.Sprintf("%s:%d", bindAddress, opts.Public.Port),
			Handler:           publicRouter,
			ReadHeaderTimeout: 30 * time.Second, // prevent slow loris
			ReadTimeout:       60 * time.Second, // max 1min per request (including watch streams)
			WriteTimeout:      60 * time.Second, // max 1min response write (forces watch reconnect)
			IdleTimeout:       90 * time.Second, // connection reuse between requests
		}

		// Configure HTTP/2 with keep-alive
		http2Server := &http2.Server{
			IdleTimeout: 90 * time.Second,
		}
		if err := http2.ConfigureServer(publicServer, http2Server); err != nil {
			return nil, fmt.Errorf("failed to configure HTTP/2: %w", err)
		}

		server.publicRouter = publicRouter
		server.publicServer = publicServer
	}

	return server, nil
}

// Run starts the API server(s).
func (s *Server) Run() error {
	errCh := make(chan error, 2)
	go func() {
		s.logger.Info("Private API server starting", "port", s.options.Private.Port)
		prepared := s.aggregatedServer.PrepareRun()
		errCh <- prepared.Run(s.stopCh)
	}()

	if s.options.Public.Enable && s.publicServer != nil {
		s.logger.Info("Public API server listening", "addr", s.publicServer.Addr)
		go func() {
			if err := s.publicServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.logger.Error(err, "Public API server error")
				errCh <- err
			}
		}()
	}

	return <-errCh
}

// Shutdown gracefully shuts down the server(s).
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})

	if s.publicServer != nil {
		if err := s.publicServer.Shutdown(ctx); err != nil {
			return err
		}
	}

	return nil
}

// PrivateAddress returns the private server's listen address.
func (s *Server) PrivateAddress() string {
	return fmt.Sprintf(":%d", s.options.Private.Port)
}

// PublicAddress returns the public server's listen address.
func (s *Server) PublicAddress() string {
	if s.publicServer != nil {
		return s.publicServer.Addr
	}
	return ""
}
