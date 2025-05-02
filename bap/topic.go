package bap

import (
	"context"
	"errors"

	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

type TopicManager struct {
	Lookup *LookupService
}

func (tm *TopicManager) IdentifyAdmissableOutputs(ctx context.Context, beefBytes []byte, previousCoins map[uint32][]byte) (admit overlay.AdmittanceInstructions, err error) {
	_, tx, _, err := transaction.ParseBeef(beefBytes)
	if err != nil {
		return admit, err
	} else if tx == nil {
		return admit, errors.New("transaction is nil")
	}

	for vout, output := range tx.Outputs {
		bc := bitcom.Decode(output.LockingScript)
		if bc == nil {
			continue
		}

		bap := bitcom.DecodeBAP(bc)
		if bap == nil {
			continue
		}
		var aip *bitcom.AIP
		for _, a := range bitcom.DecodeAIP(bc) {
			if a.Valid {
				aip = a
				break
			}
		}
		if aip == nil {
			continue
		}
		id, err := tm.Lookup.LoadIdentityByAddress(ctx, aip.Address)
		if err != nil {
			return admit, err
		}
		switch bap.Type {
		case bitcom.ID:
			if id == nil {
				admit.OutputsToAdmit = append(admit.OutputsToAdmit, uint32(vout))
			} else if id.CurrentAddress == aip.Address {
				admit.OutputsToAdmit = append(admit.OutputsToAdmit, uint32(vout))
			}

		case bitcom.ATTEST, bitcom.REVOKE, bitcom.ALIAS:
			if id != nil && id.CurrentAddress == aip.Address {
				admit.OutputsToAdmit = append(admit.OutputsToAdmit, uint32(vout))
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
