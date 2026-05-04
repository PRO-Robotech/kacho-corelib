package validate

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPageSize(t *testing.T) {
	cases := []struct {
		name      string
		input     int64
		wantEff   int64
		wantErr   bool
		wantField string
	}{
		{name: "zero -> default", input: 0, wantEff: 50, wantErr: false},
		{name: "one -> one", input: 1, wantEff: 1, wantErr: false},
		{name: "max valid 1000", input: 1000, wantEff: 1000, wantErr: false},
		{name: "negative", input: -1, wantErr: true, wantField: "page_size"},
		{name: "min-overflow", input: -100, wantErr: true, wantField: "page_size"},
		{name: "max+1", input: 1001, wantErr: true, wantField: "page_size"},
		{name: "huge", input: 10000, wantErr: true, wantField: "page_size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eff, err := PageSize("page_size", tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %d, got nil (eff=%d)", tc.input, eff)
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("expected gRPC status, got: %v", err)
				}
				if st.Code() != codes.InvalidArgument {
					t.Fatalf("expected InvalidArgument, got %v", st.Code())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if eff != tc.wantEff {
				t.Fatalf("expected eff=%d, got %d", tc.wantEff, eff)
			}
		})
	}
}
