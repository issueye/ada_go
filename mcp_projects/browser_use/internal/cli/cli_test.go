package cli

import "testing"

// TestClientFlag 钉全局 --client 的解析：它决定租约与审计归属，必须能从任意位置取值，
// 且不能把值当成子命令。
func TestClientFlag(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		wantID string
		want   []string
	}{
		{"after subcommand", []string{"call", "status", "--client", "adag-1"}, "adag-1", []string{"call", "status"}},
		{"equals form", []string{"--client=adag-2", "call", "status"}, "adag-2", []string{"call", "status"}},
		{"before subcommand", []string{"--client", "adag-3", "call", "status"}, "adag-3", []string{"call", "status"}},
		{"absent", []string{"run", "-e", "1+1"}, "", []string{"run", "-e", "1+1"}},
		{"dangling", []string{"--client"}, "", []string{}},
		{"empty value", []string{"--client="}, "", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, rest := clientFlag(c.args)
			if id != c.wantID {
				t.Fatalf("id = %q，期望 %q", id, c.wantID)
			}
			if len(rest) != len(c.want) {
				t.Fatalf("rest = %v，期望 %v", rest, c.want)
			}
			for i := range c.want {
				if rest[i] != c.want[i] {
					t.Fatalf("rest[%d] = %q，期望 %q", i, rest[i], c.want[i])
				}
			}
		})
	}
}
