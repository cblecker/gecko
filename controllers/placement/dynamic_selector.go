package placement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/iterator"
)

// mcRegistrationLabelFilter is the Secret Manager list filter used to discover
// mc-registration-* secrets created by gcp-hcp-infra
// (terraform/modules/management-cluster/region-registration.tf).
const mcRegistrationLabelFilter = "labels.mc-registration=true"

// Selector abstracts MC + DNS domain selection so the Reconciler can be tested
// without Secret Manager wiring.
type Selector interface {
	Select(ctx context.Context) (mcName, baseDomain string, err error)
}

// secretLookup abstracts Secret Manager operations so they can be replaced in
// tests without a live GCP connection.
type secretLookup interface {
	listSecrets(ctx context.Context, parent, filter string) ([]*secretmanagerpb.Secret, error)
	accessSecretVersion(ctx context.Context, name string) ([]byte, error)
}

// realSMClient adapts *secretmanager.Client to the secretLookup interface.
type realSMClient struct {
	c *secretmanager.Client
}

func (r *realSMClient) listSecrets(ctx context.Context, parent, filter string) ([]*secretmanagerpb.Secret, error) {
	it := r.c.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
		Parent: parent,
		Filter: filter,
	})
	var secrets []*secretmanagerpb.Secret
	for {
		secret, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
}

func (r *realSMClient) accessSecretVersion(ctx context.Context, name string) ([]byte, error) {
	result, err := r.c.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	})
	if err != nil {
		return nil, err
	}
	return result.Payload.Data, nil
}

// mcRegistrationPayload is the JSON content of an mc-registration-* secret
// version, written by terraform/modules/management-cluster/region-registration.tf.
type mcRegistrationPayload struct {
	ProjectID string `json:"projectId"`
	Mode      string `json:"mode"` // "active" or "maintenance"
}

// defaultCacheTTL is the default duration eligible MC results are cached before
// re-querying Secret Manager. MC registration changes are infrequent, so 30s
// staleness is acceptable and avoids quota pressure under burst reconciliations.
const defaultCacheTTL = 30 * time.Second

// DynamicSelector discovers eligible management clusters at selection time by
// reading mc-registration-* Secret Manager secrets, and round-robins across a
// statically configured list of HC DNS zone domains.
//
// MC discovery:
//  1. List SM secrets labeled mc-registration=true
//  2. Access each secret's latest version and parse the JSON payload
//     ({"projectId": "...", "mode": "active|maintenance"})
//  3. Eligible = secrets where mode == "active"
//
// DNS domains are injected at construction time (Helm value → CLI flag),
// since they are a region-level resource shared across all MCs, not
// discoverable per-MC.
//
// Selection is round-robin across eligible MCs and configured domains.
// Eligible MC results are cached for cacheTTL to reduce Secret Manager RPCs.
type DynamicSelector struct {
	smLookup secretLookup
	project  string
	domains  []string

	mcCounter  atomic.Uint64
	domCounter atomic.Uint64

	cacheMu   sync.Mutex
	cachedMCs []string
	cachedAt  time.Time
	cacheTTL  time.Duration
}

// Compile-time check: DynamicSelector implements Selector.
var _ Selector = (*DynamicSelector)(nil)

// NewDynamicSelector creates a DynamicSelector.
// project is the GCP project ID for Secret Manager lookups.
// domains is the list of HC DNS zone domains to round-robin across; it must
// be non-empty.
func NewDynamicSelector(smClient *secretmanager.Client, project string, domains []string) (*DynamicSelector, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("hc dns domains: at least one domain is required")
	}
	return &DynamicSelector{
		smLookup: &realSMClient{c: smClient},
		project:  project,
		domains:  domains,
		cacheTTL: defaultCacheTTL,
	}, nil
}

// Select discovers eligible MCs dynamically, then picks one MC and one
// configured DNS domain using round-robin counters.
func (s *DynamicSelector) Select(ctx context.Context) (mcName, baseDomain string, err error) {
	eligible, err := s.eligibleMCs(ctx)
	if err != nil {
		return "", "", fmt.Errorf("discover eligible MCs: %w", err)
	}
	if len(eligible) == 0 {
		return "", "", fmt.Errorf("no eligible management clusters found (check mc-registration secrets and mode=active)")
	}

	mc := eligible[s.mcCounter.Add(1)%uint64(len(eligible))]
	domain := s.domains[s.domCounter.Add(1)%uint64(len(s.domains))]

	return mc, domain, nil
}

// eligibleMCs returns the cached eligible MC list if still valid, otherwise
// re-queries Secret Manager. Individual secret access/unmarshal errors are
// logged and skipped — only a listSecrets failure or zero healthy secrets
// after filtering produces an error.
func (s *DynamicSelector) eligibleMCs(ctx context.Context) ([]string, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if len(s.cachedMCs) > 0 && time.Since(s.cachedAt) < s.cacheTTL {
		return s.cachedMCs, nil
	}

	eligible, err := s.fetchEligibleMCs(ctx)
	if err != nil {
		return nil, err
	}

	s.cachedMCs = eligible
	s.cachedAt = time.Now()
	return eligible, nil
}

// fetchEligibleMCs lists mc-registration-* secrets, reads each secret's latest
// version, and returns the projectId of every secret whose mode is "active".
// Errors reading or parsing individual secrets are logged and skipped.
func (s *DynamicSelector) fetchEligibleMCs(ctx context.Context) ([]string, error) {
	secrets, err := s.smLookup.listSecrets(ctx,
		fmt.Sprintf("projects/%s", s.project),
		mcRegistrationLabelFilter,
	)
	if err != nil {
		return nil, fmt.Errorf("list mc-registration secrets: %w", err)
	}

	var eligible []string
	for _, secret := range secrets {
		data, err := s.smLookup.accessSecretVersion(ctx, secret.Name+"/versions/latest")
		if err != nil {
			slog.WarnContext(ctx, "skipping mc-registration secret: access failed",
				"secret", secret.Name, "error", err)
			continue
		}

		var payload mcRegistrationPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			slog.WarnContext(ctx, "skipping mc-registration secret: invalid JSON",
				"secret", secret.Name, "error", err)
			continue
		}

		if payload.Mode == "active" && payload.ProjectID != "" {
			eligible = append(eligible, payload.ProjectID)
		}
	}
	return eligible, nil
}
