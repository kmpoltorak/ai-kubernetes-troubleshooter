package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPurgeValidatesBeforeConnecting(t *testing.T) {
	for _, o := range []PurgeOptions{
		{Limit: 100},
		{Before: time.Now().Add(time.Hour), Limit: 100},
		{Before: time.Unix(0, 0), Limit: 0},
		{Before: time.Unix(0, 0), Limit: 1001},
		{Before: time.Unix(0, 0), Limit: 100, Confirm: "yes"},
		{Before: time.Unix(0, 0), Limit: 100, Confirm: strings.Repeat("g", 64)},
	} {
		if _, err := (&Store{}).Purge(context.Background(), o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
}
