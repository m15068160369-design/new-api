package cloudflare

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
)

// [patch lou #5] pattern matrix for splitCfKey. Every input branch (4-dash, 3-dash,
// plain token, malformed) gets an assertion so regressions surface here first.
func TestSplitCfKey(t *testing.T) {
	cases := []struct {
		name             string
		key              string
		wantAcc, wantTok string
	}{
		// seller file format: 32-hex account + 4 dashes + token (synthetic, format only)
		{"4-dash real file format", "4deba88f87778c64dd0b973db5eab108----cfut_SYNTHETICt0kenForTestOnly1234567890abcdef", "4deba88f87778c64dd0b973db5eab108", "cfut_SYNTHETICt0kenForTestOnly1234567890abcdef"},
		// manual entry format
		{"3-dash manual format", "abc123---cfut_tok", "abc123", "cfut_tok"},
		// legacy: plain token, account comes from channel Other field
		{"plain token, no separator", "cfut_SPfcfZThEiUy0", "", "cfut_SPfcfZThEiUy0"},
		// tokens that legitimately contain single/double dashes must not be split
		{"single dash in token", "cfut_has-dash", "", "cfut_has-dash"},
		{"double dash in token", "cfut_has--dash", "", "cfut_has--dash"},
		// whitespace handling (multi-key textarea lines get trimmed upstream too)
		{"leading/trailing whitespace", "  acct42----cfut_tok  ", "acct42", "cfut_tok"},
		// separator at boundaries -> not splittable, whole line is the token
		{"separator at start (no account)", "---cfut_tok", "", "---cfut_tok"},
		{"separators only", "----", "", "----"},
		// separator at end (no token) falls through 4-dash, matches 3-dash
		// -> token would be "-"; acc/tok both non-empty so it splits. Malformed
		// input, request will 401 and retry moves to the next line. Accepted.
		{"separator at end (no token)", "acct----", "acct", "-"},
		// empty
		{"empty key", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc, tok := splitCfKey(tc.key)
			if acc != tc.wantAcc || tok != tc.wantTok {
				t.Fatalf("splitCfKey(%q) = (%q, %q), want (%q, %q)", tc.key, acc, tok, tc.wantAcc, tc.wantTok)
			}
		})
	}
}

// [patch lou #5] GetRequestURL must prefer the per-key account over the channel-level
// ApiVersion (Account ID field), and keep the legacy behavior for plain tokens.
func TestGetRequestURLAccountSelection(t *testing.T) {
	a := &Adaptor{}

	withKey := "4deba88f87778c64dd0b973db5eab108----cfut_tok"
	plainKey := "cfut_tok"

	tests := []struct {
		name    string
		apiKey  string
		apiVer  string
		wantURL string
	}{
		{
			name:    "per-key account wins over channel field",
			apiKey:  withKey,
			apiVer:  "channellevelacct",
			wantURL: "https://api.cloudflare.com/client/v4/accounts/4deba88f87778c64dd0b973db5eab108/ai/v1/chat/completions",
		},
		{
			name:    "plain token falls back to channel field",
			apiKey:  plainKey,
			apiVer:  "channellevelacct",
			wantURL: "https://api.cloudflare.com/client/v4/accounts/channellevelacct/ai/v1/chat/completions",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				ChannelBaseUrl: "https://api.cloudflare.com",
				ApiVersion:     tc.apiVer,
				ApiKey:         tc.apiKey,
			}}
			info.RelayMode = constant.RelayModeChatCompletions
			url, err := a.GetRequestURL(info)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if url != tc.wantURL {
				t.Fatalf("GetRequestURL = %q, want %q", url, tc.wantURL)
			}
		})
	}
}
