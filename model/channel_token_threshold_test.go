package model

// [patch lou #12] unit tests for channel input-token threshold routing.
// The env is set in init() so the sync.Once parser picks it up no matter
// which test triggers the first getChannelMinInputTokens() call.

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func init() {
	_ = os.Setenv("CHANNEL_MIN_INPUT_TOKENS", `{"11":5000,"12":0,"99":100}`)
}

func TestTokenThresholdEnvParsing(t *testing.T) {
	m := getChannelMinInputTokens()
	assert.Equal(t, 5000, m[11], "channel 11 threshold parsed")
	assert.Equal(t, 100, m[99], "channel 99 threshold parsed")
	_, has12 := m[12]
	assert.False(t, has12, "non-positive threshold entries are dropped")
	assert.Equal(t, 2, len(m), "only valid entries survive")
}

func TestTokenThresholdPassMatrix(t *testing.T) {
	// channel 11 -> 5000, channel 99 -> 100, channel 5 -> no threshold
	cases := []struct {
		channelId int
		est       int
		want      bool
		note      string
	}{
		{11, 4999, false, "below threshold -> ineligible"},
		{11, 5000, true, "exact threshold boundary -> eligible"},
		{11, 5001, true, "above threshold -> eligible"},
		{11, -1, true, "unknown estimate (fail-open) -> eligible"},
		{99, 99, false, "below smaller threshold -> ineligible"},
		{99, 100, true, "meets smaller threshold -> eligible"},
		{5, 1, true, "channel without threshold -> always eligible"},
		{5, -1, true, "channel without threshold, unknown est -> eligible"},
		{12345, 0, true, "unconfigured channel -> eligible"},
	}
	for _, c := range cases {
		got := ChannelPassesTokenThreshold(c.channelId, c.est)
		assert.Equal(t, c.want, got, "channel %d est %d: %s", c.channelId, c.est, c.note)
	}
}

func TestTokenThresholdFilterMatrix(t *testing.T) {
	// est below both thresholds: 11 and 99 dropped, 5 kept
	got := filterChannelsByTokenThreshold([]int{11, 5, 99}, 50)
	assert.Equal(t, []int{5}, got)

	// est meets 100 but not 5000: only 11 dropped
	got = filterChannelsByTokenThreshold([]int{11, 5, 99}, 150)
	assert.Equal(t, []int{5, 99}, got)

	// est meets everything: unchanged order preserved
	got = filterChannelsByTokenThreshold([]int{11, 5, 99}, 5000)
	assert.Equal(t, []int{11, 5, 99}, got)

	// unknown estimate: fail-open, nothing filtered
	got = filterChannelsByTokenThreshold([]int{11, 5, 99}, -1)
	assert.Equal(t, []int{11, 5, 99}, got)

	// all candidates filtered out: fall back to the full list (fail-open)
	got = filterChannelsByTokenThreshold([]int{11, 99}, 50)
	assert.Equal(t, []int{11, 99}, got, "empty filtered set falls back to all candidates")

	// empty input stays empty
	got = filterChannelsByTokenThreshold([]int{}, 5000)
	assert.Equal(t, []int{}, got)
}

func TestTokenThresholdAbilitiesFilterMatrix(t *testing.T) {
	ab := func(id int) Ability { return Ability{ChannelId: id} }
	base := []Ability{ab(11), ab(5), ab(99)}

	got := filterAbilitiesByTokenThreshold(base, 50)
	assert.Equal(t, []Ability{ab(5)}, got)

	got = filterAbilitiesByTokenThreshold(base, 150)
	assert.Equal(t, []Ability{ab(5), ab(99)}, got)

	got = filterAbilitiesByTokenThreshold(base, -1)
	assert.Equal(t, base, got, "unknown estimate keeps all")

	got = filterAbilitiesByTokenThreshold([]Ability{ab(11), ab(99)}, 50)
	assert.Equal(t, []Ability{ab(11), ab(99)}, got, "empty filtered set falls back")
}
