package main

import "testing"

func TestArgValueReadsBothFlagForms(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"serve", "--data-dir", `C:\Users\a\.lovkilde`, "--addr", "x"}, `C:\Users\a\.lovkilde`},
		{[]string{"serve", "--data-dir=D:\\data"}, `D:\data`},
		{[]string{"serve", "--data-dir"}, ""},
		{[]string{"serve"}, ""},
	} {
		if got := argValue(c.args, "--data-dir"); got != c.want {
			t.Errorf("%q: got %q, want %q", c.args, got, c.want)
		}
	}
}
