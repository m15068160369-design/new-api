package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemoryStorageCloseDropsReferences verifies the [backport upstream
// PR#8204] behaviour: after Close, the backing data/reader references are
// dropped so a closed-but-still-referenced storage no longer pins the body
// bytes in the heap.
func TestMemoryStorageCloseDropsReferences(t *testing.T) {
	s := newMemoryStorage([]byte("hello world"))
	require.NotNil(t, s.data)
	require.NotNil(t, s.reader)

	require.NoError(t, s.Close())
	assert.Nil(t, s.data)
	assert.Nil(t, s.reader)

	// idempotent: second Close must not panic or resurrect references
	require.NoError(t, s.Close())
	assert.Nil(t, s.data)

	// closed storage reads must fail with ErrStorageClosed, not use a nil reader
	_, err := s.Bytes()
	assert.ErrorIs(t, err, ErrStorageClosed)
	_, err = s.Read(make([]byte, 4))
	assert.ErrorIs(t, err, ErrStorageClosed)
}
