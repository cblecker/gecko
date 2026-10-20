// Package main is the golangci-lint plugin entry point for gecko's platform
// API convention linters. Compile with -buildmode=plugin.
//
// golangci-lint loads the plugin by looking up the exported symbol "New",
// which must have the signature func(any) ([]*analysis.Analyzer, error).
//
// The plugin registers the following analyzers:
//
//   - requirednoomitempty   — +required fields must not have omitempty.
//   - noimmutablemarker    — +immutable is forbidden; use CEL XValidation instead.
//   - preferstandardmarkers — prefer +default/+optional/+required/+listType over
//     kubebuilder-specific equivalents.
//   - noisprefixbool       — bool fields must not start with "Is".
//   - timestampnaming      — metav1.Time fields must end with "Time".
package main

import (
	"golang.org/x/tools/go/analysis"

	"github.com/openshift-online/gecko/hack/tools/plugin/analyzer"
)

// New is the golangci-lint v2 plugin entry point. It receives the plugin
// settings from the golangci-lint configuration (unused — this plugin has no
// configurable settings) and returns the list of analyzers to register.
func New(_ any) ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{
		analyzer.RequiredNoOmitempty,
		analyzer.NoImmutableMarker,
		analyzer.PreferStandardMarkers,
		analyzer.NoIsPrefixBool,
		analyzer.TimestampNaming,
		analyzer.ScalarBounds,
		analyzer.OptionalHasOmitempty,
	}, nil
}
