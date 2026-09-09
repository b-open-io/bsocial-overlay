package bsocial

import (
	"encoding/base64"
	"github.com/bitcoinschema/go-bmap"
	magic "github.com/bitcoinschema/go-map"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"go.mongodb.org/mongo-driver/bson"
	"os"
	"testing"
)

func TestRawTransactionPreservesMAPAndBinaryBytes(t *testing.T) {
	raw, err := os.ReadFile("testdata/map-op-return.hex")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := transaction.NewTransactionFromHex(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := bmap.NewFromTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.MAP) != 1 {
		t.Fatalf("expected MAP from real subscription transaction, got %d", len(parsed.MAP))
	}

	// Exercise the complete raw-script -> BOB -> MAP -> BSON path with binary
	// bytes, rather than constructing a parsed MAP value directly.
	data := []byte{0xff, 0x80, 0}
	lockingScript := []byte{script.OpFALSE, script.OpRETURN}
	for _, push := range [][]byte{[]byte(magic.Prefix), []byte("SET"), []byte("app"), []byte("test"), []byte("type"), []byte("message"), []byte("msg"), data} {
		lockingScript = append(lockingScript, byte(len(push)))
		lockingScript = append(lockingScript, push...)
	}
	tx.Outputs[2].LockingScript = script.NewFromBytes(lockingScript)
	parsed, err = bmap.NewFromTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.MAP) != 1 {
		t.Fatal("binary MAP was dropped")
	}
	expected := magic.BinaryValue{B: base64.StdEncoding.EncodeToString(data)}
	if parsed.MAP[0]["msg"] != expected {
		t.Fatalf("binary bytes changed: %#v", parsed.MAP[0]["msg"])
	}
	doc, err := PrepareForIngestion(parsed)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var restored bson.M
	if err := bson.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	maps := restored["MAP"].(bson.A)
	value := maps[0].(bson.M)["msg"].(bson.M)["b"]
	if value != expected.B {
		t.Fatalf("stored bytes changed: %v", value)
	}
}
