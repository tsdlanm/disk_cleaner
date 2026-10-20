package format

import "testing"

func TestBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024*1024 - 1, "1.0 MB"},
		{5 << 30, "5.0 GB"},
		{3 << 40, "3.0 TB"},
	}
	for _, c := range cases {
		if got := Bytes(c.in); got != c.want {
			t.Errorf("Bytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInt(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1.000", 1204311: "1.204.311", -1500: "-1.500"}
	for in, want := range cases {
		if got := Int(in); got != want {
			t.Errorf("Int(%d) = %q, want %q", in, got, want)
		}
	}
}
