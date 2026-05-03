package ids

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewUID_ReturnsValidUUIDv4(t *testing.T) {
	id := NewUID()
	parsed, err := uuid.Parse(id)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), parsed.Version())
}

func TestNewUID_Unique(t *testing.T) {
	a, b := NewUID(), NewUID()
	require.NotEqual(t, a, b)
}
