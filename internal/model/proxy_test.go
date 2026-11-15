package model

import "testing"

func TestNormalizeProxyURL(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"http://proxy.example:8080", "http://proxy.example:8080"},
		{"https://proxy.example:8443", "https://proxy.example:8443"},
		{"socks://proxy.example:1080", "socks://proxy.example:1080"},
		{"socks5://proxy.example:1080", "socks5://proxy.example:1080"},
		{" SOCKS5H://Alice:p%40ss@Proxy.Example:1080 ", "socks5h://Alice:p%40ss@proxy.example:1080"},
		{"socks5h://[::1]:1080", "socks5h://[::1]:1080"},
		{"socks5h://proxy.example", "socks5h://proxy.example"},
		{"", ""},
		{"socks5h://", ""},
		{"socks4://proxy.example:1080", ""},
		{"ftp://proxy.example", ""},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := NormalizeProxyURL(test.input)
			if test.want == "" {
				if err == nil {
					t.Fatal("invalid proxy URL accepted")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("NormalizeProxyURL() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}
