package bsocial

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
)

func TestFetchBeefRetriesAndHonorsCancellation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte{1, 2, 3})
	}))
	defer server.Close()
	id := &chainhash.Hash{}
	if _, err := FetchBeef(context.Background(), server.Client(), server.URL, id); err == nil {
		t.Fatal("accepted failed HTTP response")
	}
	data, err := FetchBeef(context.Background(), server.Client(), server.URL, id)
	if err != nil || len(data) != 3 || calls != 2 {
		t.Fatalf("failed request was not retried: %v, calls %d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchBeef(ctx, server.Client(), server.URL, id); err == nil {
		t.Fatal("ignored cancellation")
	}
	if calls != 2 {
		t.Fatal("cancelled request reached server")
	}
}
