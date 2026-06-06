package sapclient

import (
	"context"
	"net"
	"testing"
)

func TestIsTransientNetworkErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "dns timeout",
			err:  &net.DNSError{IsTimeout: true},
			want: true,
		},
		{
			name: "dns temporary",
			err:  &net.DNSError{IsTemporary: true},
			want: true,
		},
		{
			name: "dns permission denied — the exact production bug",
			err:  &net.DNSError{Err: "write udp 128.128.0.3:53444 -> 192.168.31.53: write: permission denied"},
			want: true,
		},
		{
			name: "dns operation not permitted",
			err:  &net.DNSError{Err: "operation not permitted"},
			want: true,
		},
		{
			name: "dns server misbehaving (not temporary, not a known string)",
			err:  &net.DNSError{Err: "server misbehaving"},
			want: false,
		},
		{
			name: "non-dns error",
			err:  net.ErrClosed,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientNetworkErr(tc.err)
			if got != tc.want {
				t.Errorf("isTransientNetworkErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
