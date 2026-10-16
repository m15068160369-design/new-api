package common

import "testing"

// [patch lou #6 v6] FakeCacheTokens 边界表：小 p（≤~1003）不注入归零保留真实，
// 双参 a·p−D 全区间单调
func TestFakeCacheTokens(t *testing.T) {
	cases := []struct {
		p    int
		want int
	}{
		{0, 0}, {1, 0}, {1000, 0}, {1004, 0}, {1010, 5},
		{2000, 994}, {5000, 3991}, {10000, 8985}, {20000, 18973},
		{50000, 48937}, {100000, 98877}, {250000, 248697}, {1000000, 997797},
	}
	for _, c := range cases {
		if got := FakeCacheTokens(c.p); got != c.want {
			t.Fatalf("p=%d want %d got %d", c.p, c.want, got)
		}
	}
	// 不变量：非负、≤p、随 p 单调不减（v6 起 p<10k 可低于 v3 的 0.9p，属拍板放宽）
	prev := -1
	for p := 1; p <= 1000000; p = p*3 + 7 {
		got := FakeCacheTokens(p)
		if got < 0 || got > p || got < prev {
			t.Fatalf("p=%d 注入值 %d 违反 [0,p] 或单调性", p, got)
		}
		prev = got
	}
}
