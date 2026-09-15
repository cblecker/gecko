package nodepool

import (
	"fmt"

	"github.com/spf13/cobra"

	fstransport "github.com/openshift-online/gecko/controllers/client/transport/firestore"
	nodepool "github.com/openshift-online/gecko/controllers/nodepool"
	"github.com/openshift-online/gecko/controllers/util/setup"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	ctrl "sigs.k8s.io/controller-runtime"
)

// NewCommand returns the nodepool subcommand.
func NewCommand(rf *setup.RootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nodepool",
		Short: "Run the nodepool controller",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			log, err := rf.NewLogger("nodepool-controller")
			if err != nil {
				return fmt.Errorf("create logger: %w", err)
			}

			t := fstransport.New(log)
			defer t.Close()

			scheme := setup.NewScheme()
			mgr, err := rf.NewManager(scheme, log)
			if err != nil {
				return fmt.Errorf("create manager: %w", err)
			}

			rec := nodepool.New(t, log, mgr.GetClient())

			if err := ctrl.NewControllerManagedBy(mgr).
				For(&privatev1.NodePool{}).
				WithOptions(rf.ControllerOpts()).
				Complete(rec); err != nil {
				return fmt.Errorf("setup controller: %w", err)
			}

			return mgr.Start(ctx)
		},
	}

	return cmd
}
