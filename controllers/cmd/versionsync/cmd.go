package versionsync

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/openshift-online/gecko/controllers/util/setup"
	"github.com/openshift-online/gecko/controllers/versionresolution"
	versionsynccontroller "github.com/openshift-online/gecko/controllers/versionsync"
)

// NewCommand returns the version-sync subcommand.
func NewCommand(rootFlags *setup.RootFlags) *cobra.Command {
	var cincinnatiURL string
	var architecture string
	var sourceType string
	var sourceURL string

	command := &cobra.Command{
		Use:   "version-sync",
		Short: "Synchronize the OpenShift version catalog",
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			if sourceType != "cincinnati" && sourceType != "release-controller" {
				return fmt.Errorf("unsupported source type %q: use cincinnati or release-controller", sourceType)
			}
			var ciSource *versionsynccontroller.ReleaseControllerClient
			if sourceType == "release-controller" {
				var err error
				ciSource, err = versionsynccontroller.NewReleaseControllerClient(sourceURL)
				if err != nil {
					return err
				}
			}

			log, err := rootFlags.NewLogger("version-sync-controller")
			if err != nil {
				return fmt.Errorf("create logger: %w", err)
			}

			scheme := setup.NewScheme()
			manager, err := rootFlags.NewManager(scheme, log)
			if err != nil {
				return fmt.Errorf("create manager: %w", err)
			}

			if sourceURL != "" && sourceType == "cincinnati" {
				cincinnatiURL = sourceURL
			}
			cincinnatiClient := versionresolution.NewCincinnatiClient(
				cincinnatiURL,
				architecture,
			)

			controller := versionsynccontroller.NewController(
				cincinnatiClient,
				log,
				manager.GetClient(),
			)

			if ciSource != nil {
				controller = versionsynccontroller.NewCIController(ciSource, log, manager.GetClient())
			}

			if err := manager.Add(controller); err != nil {
				return fmt.Errorf("add version-sync controller: %w", err)
			}

			return manager.Start(ctx)
		},
	}

	command.Flags().StringVar(
		&cincinnatiURL,
		"cincinnati-url",
		"https://api.openshift.com/api/upgrades_info/v1/graph",
		"Cincinnati API URL",
	)
	command.Flags().StringVar(
		&architecture,
		"arch",
		"amd64",
		"CPU architecture for Cincinnati queries",
	)

	command.Flags().StringVar(&sourceType, "source-type", "cincinnati", "Catalog source: cincinnati or release-controller")
	command.Flags().StringVar(&sourceURL, "source-url", "", "Catalog endpoint: Cincinnati graph URL or architecture-specific release-controller base URL (required for release-controller; overrides --cincinnati-url)")

	return command
}
