package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestLiveResearchRaw(t *testing.T) {
	if os.Getenv("IPV6_LIVE_TEST") != "1" {
		t.Skip("explicit network audit only")
	}
	a := testApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", "https://raw.githubusercontent.com/python/cpython/main/README.rst", nil)
	res, e := a.transport.RoundTrip(r)
	if e != nil {
		t.Fatalf("%T %v", e, e)
	}
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	t.Log(res.StatusCode, len(b), e)
}
