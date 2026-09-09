package bsocial

import (
	"context"
	"testing"

	magic "github.com/bitcoinschema/go-map"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

func TestTopicAdmitsPublishedSocialActions(t *testing.T) {
	for _, kind := range []string{"post", "message", "like", "unlike", "follow", "unfollow", "friend", "unfriend", "repost", "video"} {
		t.Run(kind, func(t *testing.T) {
			raw := []byte{script.OpFALSE, script.OpRETURN}
			for _, value := range []string{magic.Prefix, "SET", "app", "bsv-mcp", "type", kind} {
				raw = append(raw, byte(len(value)))
				raw = append(raw, []byte(value)...)
			}
			tx := transaction.NewTransaction()
			tx.Outputs = append(tx.Outputs, &transaction.TransactionOutput{Satoshis: 0, LockingScript: script.NewFromBytes(raw)})
			data, err := tx.AtomicBEEF(false)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (&TopicManager{}).IdentifyAdmissibleOutputs(context.Background(), data, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.OutputsToAdmit) != 1 || got.OutputsToAdmit[0] != 0 {
				t.Fatalf("%s was not admitted: %v", kind, got.OutputsToAdmit)
			}
		})
	}
}
