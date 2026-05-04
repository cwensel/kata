package main

import (
	"testing"
	"time"
)

func TestEnvHTTPTimeout(t *testing.T) {
	const def = 5 * time.Second

	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{name: "empty returns default", env: "", want: def},
		{name: "valid override", env: "30s", want: 30 * time.Second},
		{name: "minutes parse", env: "2m", want: 2 * time.Minute},
		{name: "garbage falls back", env: "not-a-duration", want: def},
		{name: "zero falls back", env: "0s", want: def},
		{name: "negative falls back", env: "-10s", want: def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KATA_HTTP_TIMEOUT", tc.env)
			got := envHTTPTimeout(def)
			if got != tc.want {
				t.Fatalf("envHTTPTimeout(%q) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}
