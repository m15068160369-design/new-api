package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [patch lou #21] 提前释放 helper 的行为单测：
// 幂等性、引用清理完整性、无 storage/无 closer 时的安全降级。

func newReleaseTestContext(t *testing.T, withStorage bool) *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if withStorage {
		storage, err := common.CreateBodyStorage([]byte(`{"model":"m","messages":[]}`))
		require.NoError(t, err)
		c.Set(common.KeyBodyStorage, storage)
		c.Request.Body = io.NopCloser(storage)
	}
	return c
}

func TestReleaseUpstreamRequestResources(t *testing.T) {
	t.Run("storage closed and references dropped", func(t *testing.T) {
		c := newReleaseTestContext(t, true)
		v, _ := c.Get(common.KeyBodyStorage)
		storage := v.(common.BodyStorage)

		releaseUpstreamRequestResources(c, nil)

		assert.ErrorIs(t, storage.Close(), nil) // 二次 Close 无错（幂等）
		_, err := storage.Bytes()
		assert.ErrorIs(t, err, common.ErrStorageClosed, "Bytes after release must fail closed")
		v, exists := c.Get(common.KeyBodyStorage)
		assert.True(t, exists, "key retained")
		assert.Nil(t, v, "storage reference dropped")
		assert.Equal(t, http.NoBody, c.Request.Body, "request body chain severed")
	})

	t.Run("idempotent across repeated calls", func(t *testing.T) {
		c := newReleaseTestContext(t, true)
		require.NotPanics(t, func() {
			releaseUpstreamRequestResources(c, nil)
			releaseUpstreamRequestResources(c, nil)
			releaseUpstreamRequestResources(c, nil)
		})
	})

	t.Run("no storage: safe no-op", func(t *testing.T) {
		c := newReleaseTestContext(t, false)
		require.NotPanics(t, func() {
			releaseUpstreamRequestResources(c, nil)
		})
		assert.NotNil(t, c.Request, "context untouched")
	})

	t.Run("outbound closer closed first when present", func(t *testing.T) {
		c := newReleaseTestContext(t, true)
		tc := &trackCloser{}
		releaseUpstreamRequestResources(c, tc)
		assert.True(t, tc.closed, "outbound closer invoked")
	})

	t.Run("outbound closer nil-safe", func(t *testing.T) {
		c := newReleaseTestContext(t, false)
		var nilCloser io.Closer = nil
		require.NotPanics(t, func() {
			releaseUpstreamRequestResources(c, nilCloser)
		})
	})
}

type trackCloser struct {
	closed bool
}

func (t *trackCloser) Close() error {
	t.closed = true
	return nil
}
