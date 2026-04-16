package ids

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNew_UniqueAndTimeOrdered(t *testing.T) {
	a := New()
	time.Sleep(2 * time.Millisecond)
	b := New()

	require.NotEqual(t, a, b)
	require.Len(t, a, 36) // standard uuid string length
	require.True(t, a < b, "UUIDv7 ids must be lexicographically time-ordered: %s < %s", a, b)
}
