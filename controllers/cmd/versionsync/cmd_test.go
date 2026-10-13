package versionsync

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvalidSourceFailsBeforeManagerSetup(t *testing.T) {
	for _, args := range [][]string{
		{"--source-type=typo"},
		{"--source-type=release-controller"},
		{"--source-type=release-controller", "--source-url=invalid"},
	} {
		t.Run(args[0], func(t *testing.T) {
			command := NewCommand(nil)
			command.SetArgs(args)
			require.Error(t, command.Execute())
		})
	}
}
