package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [backport upstream #6949] relayResponseHeaderTimeout: 0 disables, normal
// values pass through, overflow clamps to maxTimeoutSeconds.
func TestRelayResponseHeaderTimeoutHelper(t *testing.T) {
	orig := common.RelayResponseHeaderTimeout
	t.Cleanup(func() { common.RelayResponseHeaderTimeout = orig })

	t.Run("disabled keeps old unbounded behaviour", func(t *testing.T) {
		common.RelayResponseHeaderTimeout = 0
		assert.Equal(t, time.Duration(0), relayResponseHeaderTimeout())
	})

	t.Run("negative treated as disabled", func(t *testing.T) {
		common.RelayResponseHeaderTimeout = -5
		assert.Equal(t, time.Duration(0), relayResponseHeaderTimeout())
	})

	t.Run("normal value passes through", func(t *testing.T) {
		common.RelayResponseHeaderTimeout = 1800
		assert.Equal(t, 1800*time.Second, relayResponseHeaderTimeout())
	})

	t.Run("overflow clamps instead of wrapping", func(t *testing.T) {
		common.RelayResponseHeaderTimeout = int(^uint(0) >> 1) // MaxInt on this arch
		assert.Equal(t, time.Duration(maxTimeoutSeconds)*time.Second, relayResponseHeaderTimeout())
	})
}

// [backport upstream #6949] InitHttpClient must apply the header-wait bound to
// the non-stream transport and to both proxy transports; the stream
// transport keeps its own tuned 60s RHT. Serialised: mutates globals.
func TestInitHttpClientAppliesResponseHeaderTimeout(t *testing.T) {
	orig := common.RelayResponseHeaderTimeout
	t.Cleanup(func() {
		common.RelayResponseHeaderTimeout = orig
		ResetProxyClientCache()
		InitHttpClient()
	})

	common.RelayResponseHeaderTimeout = 1800
	InitHttpClient()

	nonStream, ok := GetHttpClient().Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 1800*time.Second, nonStream.ResponseHeaderTimeout)

	stream, ok := GetStreamHttpClient().Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 60*time.Second, stream.ResponseHeaderTimeout, "stream keeps own tuned RHT")

	proxyClient, err := NewProxyHttpClient("http://127.0.0.1:3128")
	require.NoError(t, err)
	proxyTransport, ok := proxyClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 1800*time.Second, proxyTransport.ResponseHeaderTimeout)

	// disabled env → non-stream/proxy stay unbounded (old behaviour)
	common.RelayResponseHeaderTimeout = 0
	InitHttpClient()
	nonStream, ok = GetHttpClient().Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, time.Duration(0), nonStream.ResponseHeaderTimeout)
	stream, ok = GetStreamHttpClient().Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 60*time.Second, stream.ResponseHeaderTimeout, "stream RHT unaffected by disable")
}
