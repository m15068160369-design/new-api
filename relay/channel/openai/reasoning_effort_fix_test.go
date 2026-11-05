package openai

import "testing"

// [patch lou #13] Unit matrix for the reasoning_effort gateway fix.
// The valid set mirrors what GLM/魔搭/商汤 upstreams actually accept
// ("field ReasoningEffort invalid, should be one of: low, high, max").
func TestIsOpenAICompatValidEffort(t *testing.T) {
	cases := []struct {
		name   string
		effort string
		want   bool
	}{
		// upstream-accepted levels → transparent
		{"low", "low", true},
		{"high", "high", true},
		{"max", "max", true},
		// casing tolerance
		{"Max", "Max", true},
		{"HIGH", "HIGH", true},
		// Hermes main-loop default → must be flagged (the original 400)
		{"medium", "medium", false},
		// Hermes title-generation disable encoding → must be flagged
		{"none", "none", false},
		// other wire vocabularies that GLM-family upstreams reject
		{"minimal", "minimal", false},
		{"default", "default", false},
		{"xhigh", "xhigh", false},
		{"ultra", "ultra", false},
		{"empty", "", false},
		{"garbage", "banana", false},
		{"whitespace", " ", false},
		{"mixed", "MAX ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isOpenAICompatValidEffort(c.effort); got != c.want {
				t.Errorf("isOpenAICompatValidEffort(%q) = %v, want %v", c.effort, got, c.want)
			}
		})
	}
}

// The integration point: ConvertOpenAIRequest must rewrite an invalid effort
// to "max" and leave valid ones untouched. ConvertOpenAIRequest needs a full
// gin context, so the rewrite rule itself is asserted on the guarded call
// shape used in the adaptor (effort != "" && !valid → "max"); this pins the
// semantics of both branches in one place.
func TestEffortFixRewriteRule(t *testing.T) {
	fix := func(effort string) string {
		if effort != "" && !isOpenAICompatValidEffort(effort) {
			return "max"
		}
		return effort
	}
	cases := []struct {
		in   string
		want string
	}{
		{"low", "low"},    // valid → untouched
		{"high", "high"},  // valid → untouched
		{"max", "max"},    // valid → untouched
		{"", ""},          // unset → field omitted (omitempty), untouched
		{"medium", "max"}, // Hermes default → max
		{"none", "max"},   // Hermes title call → max
		{"banana", "max"}, // garbage → max
	}
	for _, c := range cases {
		if got := fix(c.in); got != c.want {
			t.Errorf("fix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
