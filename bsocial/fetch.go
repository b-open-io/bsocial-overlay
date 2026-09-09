package bsocial

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bsv-blockchain/go-sdk/chainhash"
)

// FetchBeef fetches one transaction without retaining every historical response in memory.
// The overlay engine verifies the BEEF and its proof before admission.
func FetchBeef(ctx context.Context, client *http.Client, baseURL string, txid *chainhash.Hash) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/transaction/beef/"+txid.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("BEEF fetch HTTP %d", resp.StatusCode)
	}
	const maxSize = 32 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("BEEF response exceeds 32 MiB")
	}
	return data, nil
}
