package analyzer_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/openshift-online/gecko/hack/tools/plugin/analyzer"
)

func TestRequiredNoOmitempty(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer.RequiredNoOmitempty, "reqomitempty")
}

func TestNoImmutableMarker(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer.NoImmutableMarker, "noimmutable")
}

func TestPreferStandardMarkers(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer.PreferStandardMarkers, "prefmarkers")
}

func TestNoIsPrefixBool(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer.NoIsPrefixBool, "noisprefix")
}

func TestTimestampNaming(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer.TimestampNaming, "tsname")
}

func TestScalarBounds(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer.ScalarBounds, "scalarbounds")
}

func TestOptionalHasOmitempty(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer.OptionalHasOmitempty, "optomitempty")
}
