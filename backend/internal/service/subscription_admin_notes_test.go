//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAdminSubscriptionNotes(t *testing.T) {
	got, err := normalizeAdminSubscriptionNotes("  上游故障补偿\n第二行\t \n")
	require.NoError(t, err)
	require.Equal(t, "上游故障补偿\n第二行", got)

	got, err = normalizeAdminSubscriptionNotes("  " + strings.Repeat("备", 500) + "  ")
	require.NoError(t, err, "length is counted after trimming, in characters")
	require.Len(t, []rune(got), 500)

	_, err = normalizeAdminSubscriptionNotes(strings.Repeat("备", 501))
	require.ErrorIs(t, err, ErrAdminNotesTooLong)

	for _, bad := range []string{"a\x00b", "a\x1bb", "a‮b", "a⁦b", "a‏b", "a\u0085b"} {
		_, err = normalizeAdminSubscriptionNotes(bad)
		require.ErrorIs(t, err, ErrAdminNotesInvalid, "%q", bad)
	}
}
