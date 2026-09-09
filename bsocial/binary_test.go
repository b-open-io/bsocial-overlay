package bsocial

import (
	"encoding/base64"
	"encoding/json"
	b "github.com/bitcoinschema/go-b"
	bmap "github.com/bitcoinschema/go-bmap"
	magic "github.com/bitcoinschema/go-map"
	"go.mongodb.org/mongo-driver/bson"
	"testing"
)

func TestIngestionPreservesBinaryContent(t *testing.T) {
	raw := []byte{0xff, 0, 0x80, 1}
	for _, encoding := range []string{"binary", "gzip", "utf-8"} {
		tx := &bmap.Tx{B: []*b.B{{Data: raw, MediaType: "text/plain", Encoding: encoding}}, MAP: []magic.MAP{{"type": "message", "app": "test", "msg": magic.BinaryValue{B: base64.StdEncoding.EncodeToString(raw)}}}}
		doc, err := PrepareForIngestion(tx)
		if err != nil {
			t.Fatal(err)
		}
		content := doc["B"].([]bson.M)[0]
		if content["content"] != base64.StdEncoding.EncodeToString(raw) {
			t.Fatal("binary content lost")
		}
		if encoding == "utf-8" && content["encoding"] != "binary" {
			t.Fatal("invalid UTF8 advertised as text")
		}
		bytes, err := bson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var decoded bson.M
		if err = bson.Unmarshal(bytes, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err = json.Marshal(decoded); err != nil {
			t.Fatal(err)
		}
	}
	text := "hello 👋"
	tx := &bmap.Tx{B: []*b.B{{Data: []byte(text), MediaType: "application/octet-stream", Encoding: "utf-8"}}, MAP: []magic.MAP{{"type": "message", "app": "test"}}}
	doc, err := PrepareForIngestion(tx)
	if err != nil {
		t.Fatal(err)
	}
	if doc["B"].([]bson.M)[0]["content"] != text {
		t.Fatal("declared UTF8 content changed")
	}
}

func TestLegacyBinaryPreservesOriginalBytes(t *testing.T) {
	raw := []byte{0xff, 0x80, 0, 0xd8, 0x3d}
	original := bson.A{bson.M{"app": "test", "msg": string(raw), "nested": bson.A{"valid 👋", string(raw)}}}
	safe, n := PreserveLegacyMAPBinary(original)
	if n != 2 {
		t.Fatalf("expected 2 binary values, got %d", n)
	}
	entry := safe.(bson.A)[0].(bson.M)
	if entry["msg"].(magic.BinaryValue).B != base64.StdEncoding.EncodeToString(raw) {
		t.Fatal("lost source bytes")
	}
	if original[0].(bson.M)["msg"].(string) != string(raw) {
		t.Fatal("mutated original backup")
	}
	encoded, err := bson.Marshal(bson.M{"MAP": safe})
	if err != nil {
		t.Fatal(err)
	}
	var result bson.M
	if err = bson.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result["MAP"].(bson.A)[0].(bson.M)["msg"].(bson.M)["b"] != base64.StdEncoding.EncodeToString(raw) {
		t.Fatal("BSON round trip failed")
	}
}
