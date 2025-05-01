package bsocial

import (
	"context"
	"errors"
	"slices"

	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

type TopicManager struct {
	// Storage *BAPStorage
}

var mapTypes = []string{"post", "reply", "like", "unlike", "follow", "unfollow", "message"}

func (tm *TopicManager) IdentifyAdmissableOutputs(ctx context.Context, beefBytes []byte, previousCoins map[uint32][]byte) (admit overlay.AdmittanceInstructions, err error) {
	_, tx, _, err := transaction.ParseBeef(beefBytes)
	if err != nil {
		return admit, err
	} else if tx == nil {
		return admit, errors.New("transaction is nil")
	}

	for vout, output := range tx.Outputs {
		if bc := bitcom.Decode(output.LockingScript); bc != nil {
			for _, proto := range bc.Protocols {
				if proto.Protocol == bitcom.MapPrefix {
					if m := bitcom.DecodeMap(proto.Script); m == nil {
						continue
					} else if t, ok := m.Data["type"]; ok && slices.Contains(mapTypes, t) {
						admit.OutputsToAdmit = append(admit.OutputsToAdmit, uint32(vout))
						break
					}
				}
			}
		}
	}

	return
}

func (tm *TopicManager) IdentifyNeededInputs(ctx context.Context, beefBytes []byte) (neededInputs []*overlay.Outpoint, err error) {
	return neededInputs, nil
}

func (tm *TopicManager) GetDocumentation() string {
	return "BAP Topic Manager"
}

func (tm *TopicManager) GetMetaData() *overlay.MetaData {
	return &overlay.MetaData{
		Name: "bap",
	}
}
