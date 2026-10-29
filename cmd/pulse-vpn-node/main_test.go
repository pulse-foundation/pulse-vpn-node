package main

import "testing"

func TestHealthcheckAddressPreservesExplicitBindHost(t *testing.T) {
	for _, tc := range []struct{ listen, want string }{
		{"127.0.0.2:8080", "127.0.0.2:8080"},
		{"[::1]:8080", "[::1]:8080"},
		{"0.0.0.0:8080", "127.0.0.1:8080"},
		{"[::]:8080", "[::1]:8080"},
		{":8080", "127.0.0.1:8080"},
	} {
		t.Run(tc.listen, func(t *testing.T) {
			got, err := healthcheckAddress(tc.listen)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := healthcheckAddress("invalid"); err == nil {
		t.Fatal("accepted invalid bind address")
	}
}
