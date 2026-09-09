package bsocial

import (
	"encoding/base64"
	magic "github.com/bitcoinschema/go-map"
	"go.mongodb.org/mongo-driver/bson"
	"unicode/utf8"
)

// PreserveLegacyMAPBinary converts only invalid UTF-8 strings in legacy MAP data.
// Go retains the original bytes when decoding BSON strings; do not marshal them
// through JSON first, since JSON would replace those bytes with U+FFFD.
func PreserveLegacyMAPBinary(value interface{}) (interface{}, int) {
	switch v := value.(type) {
	case string:
		if !utf8.ValidString(v) {
			return magic.BinaryValue{B: base64.StdEncoding.EncodeToString([]byte(v))}, 1
		}
	case bson.M:
		result := bson.M{}
		count := 0
		for key, item := range v {
			safe, n := PreserveLegacyMAPBinary(item)
			result[key] = safe
			count += n
		}
		return result, count
	case bson.A:
		result := make(bson.A, len(v))
		count := 0
		for i, item := range v {
			safe, n := PreserveLegacyMAPBinary(item)
			result[i] = safe
			count += n
		}
		return result, count
	}
	return value, 0
}
