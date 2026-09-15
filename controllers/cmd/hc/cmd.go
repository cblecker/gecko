package hc

import (
	"fmt"

	"github.com/spf13/cobra"

	fstransport "github.com/openshift-online/gecko/controllers/client/transport/firestore"
	hc "github.com/openshift-online/gecko/controllers/hc"
	"github.com/openshift-online/gecko/controllers/util/setup"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	ctrl "sigs.k8s.io/controller-runtime"
)

// NewCommand returns the hc subcommand.
func NewCommand(rf *setup.RootFlags) *cobra.Command {
	var customerLabelsFile string

	cmd := &cobra.Command{
		Use:   "hc",
		Short: "Run the hosted-cluster (hc) controller",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			log, err := rf.NewLogger("hc-controller")
			if err != nil {
				return fmt.Errorf("create logger: %w", err)
			}

			var customerLabels map[string]string
			if customerLabelsFile != "" {
				customerLabels, err = hc.LoadCustomerLabels(customerLabelsFile)
				if err != nil {
					return fmt.Errorf("load customer labels: %w", err)
				}
			}

			t := fstransport.New(log)
			defer t.Close()

			scheme := setup.NewScheme()
			mgr, err := rf.NewManager(scheme, log)
			if err != nil {
				return fmt.Errorf("create manager: %w", err)
			}

			rec := hc.New(t, log, mgr.GetClient(), customerLabels)

			if err := ctrl.NewControllerManagedBy(mgr).
				For(&privatev1.Cluster{}).
				WithOptions(rf.ControllerOpts()).
				Complete(rec); err != nil {
				return fmt.Errorf("setup controller: %w", err)
			}

			return mgr.Start(ctx)
		},
	}

	cmd.Flags().StringVar(&customerLabelsFile, "customer-labels-file", "", "Path to JSON file containing customer-facing GCP resource labels (omit to disable)")

	return cmd
}
