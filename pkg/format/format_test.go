package format

import "testing"

func TestNextToken(t *testing.T) {
	cases := []struct{ old, epoch, want int64 }{
		{0, 0, 1},
		{7, 0, 8},
		{7, 3, 3<<32 + 1},
		{3<<32 + 1, 3, 3<<32 + 2},
		{3<<32 + 5, 4, 4<<32 + 1},
	}
	for _, c := range cases {
		if got := NextToken(c.old, c.epoch); got != c.want {
			t.Errorf("NextToken(%d, %d) = %d, want %d", c.old, c.epoch, got, c.want)
		}
	}
}
