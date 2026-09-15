package placement

import (
	"fmt"
	"os"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/spf13/cobra"

	placement "github.com/openshift-online/gecko/controllers/placement"
	"github.com/openshift-online/gecko/controllers/util/setup"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	ctrl "sigs.k8s.io/controller-runtime"
)

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// parseDomains splits a comma-separated domain list, trimming whitespace and
// dropping empty entries.
func parseDomains(csv string) []string {
	var domains []string
	for _, d := range strings.Split(csv, ",") {
		if d = strings.TrimSpace(d); d != "" {
			domains = append(domains, d)
		}
	}
	return domains
}

// NewCommand returns the placement subcommand.
func NewCommand(rf *setup.RootFlags) *cobra.Command {
	var smProject, hcDNSDomains string

	cmd := &cobra.Command{
		Use:   "placement",
		Short: "Run the placement controller",
		RunE: func(cmd *cobra.Command, args []string) error {
			if v := envOr("SECRETMANAGER_PROJECT", ""); v != "" && !cmd.Flags().Changed("secretmanager-project") {
				smProject = v
			}
			if smProject == "" {
				return fmt.Errorf("--secretmanager-project is required (or $SECRETMANAGER_PROJECT)")
			}

			ctx := cmd.Context()

			log, err := rf.NewLogger("placement-controller")
			if err != nil {
				return fmt.Errorf("create logger: %w", err)
			}

			smClient, err := secretmanager.NewClient(ctx)
			if err != nil {
				return fmt.Errorf("create secret manager client: %w", err)
			}
			defer smClient.Close() //nolint:errcheck

			selector, err := placement.NewDynamicSelector(smClient, smProject, parseDomains(hcDNSDomains))
			if err != nil {
				return fmt.Errorf("create selector: %w", err)
			}

			scheme := setup.NewScheme()
			mgr, err := rf.NewManager(scheme, log)
			if err != nil {
				return fmt.Errorf("create manager: %w", err)
			}

			rec := placement.NewReconciler(selector, log, mgr.GetClient())

			if err := ctrl.NewControllerManagedBy(mgr).
				For(&privatev1.Cluster{}).
				WithOptions(rf.ControllerOpts()).
				Complete(rec); err != nil {
				return fmt.Errorf("setup controller: %w", err)
			}

			return mgr.Start(ctx)
		},
	}

	cmd.Flags().StringVar(&smProject, "secretmanager-project", "", "GCP project for Secret Manager mc-registration discovery [$SECRETMANAGER_PROJECT] (required)")
	cmd.Flags().StringVar(&hcDNSDomains, "hc-dns-domains", "", "Comma-separated list of HC DNS zone domains to round-robin across")

	return cmd
}
