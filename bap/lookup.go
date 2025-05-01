package bap

import (
	"context"
	"fmt"

	"github.com/b-open-io/overlay/lookup/events"
	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

type LookupService struct {
	*events.RedisEventLookup
	storage *BAPStorage
}

func NewLookupService(connString string, storage *BAPStorage, topic string) (*LookupService, error) {
	if eventsLookup, err := events.NewRedisEventLookup(connString, storage, topic); err != nil {
		return nil, err
	} else {
		return &LookupService{
			RedisEventLookup: eventsLookup,
			storage:          storage,
		}, nil
	}
}

func (l *LookupService) OutputAdded(ctx context.Context, outpoint *overlay.Outpoint, topic string, beef []byte) error {
	_, tx, _, err := transaction.ParseBeef(beef)
	if err != nil {
		return err
	}
	output := tx.Outputs[outpoint.OutputIndex]
	bc := bitcom.Decode(output.LockingScript)
	if bc == nil {
		return nil
	}
	var height uint32
	if tx.MerklePath != nil {
		height = tx.MerklePath.BlockHeight
	}

	bap := bitcom.DecodeBAP(bc)
	if bap == nil {
		return nil
	}
	var aip *bitcom.AIP
	for _, a := range bitcom.DecodeAIP(bc) {
		if a.Valid {
			aip = a
			break
		}
	}
	if aip == nil {
		return nil
	}
	id, err := l.storage.LoadIdentityByAddress(ctx, aip.Address)
	if err != nil {
		return err
	}
	switch bap.Type {
	case bitcom.ID:
		if id == nil {
			id = &Identity{
				IDKey:          bap.IDKey,
				RootAddress:    aip.Address,
				CurrentAddress: bap.Address,
				Addresses: []Address{
					{
						Address: bap.Address,
						Txid:    outpoint.Txid,
						Block:   height,
					},
				},
				FirstSeen: height,
			}
		} else {
			id.CurrentAddress = aip.Address
			id.Addresses = append(id.Addresses, Address{
				Address: bap.Address,
				Txid:    outpoint.Txid,
				Block:   height,
			})
		}
		if err := l.storage.SaveIdentity(ctx, id); err != nil {
			return err
		}
	case bitcom.ATTEST:
		if id == nil {
			return fmt.Errorf("identity not found for address %s", aip.Address)
		}
		signer := Signer{
			IDKey:   id.IDKey,
			Address: aip.Address,
			Txid:    outpoint.Txid,
			Revoked: false,
		}
		attest, err := l.storage.LoadAttestation(ctx, bap.IDKey)
		if err != nil {
			return err
		} else if attest == nil {
			attest = &Attestation{
				Id:      bap.IDKey,
				Signers: []*Signer{&signer},
			}
		} else {
			attest.Signers = append(attest.Signers, &signer)
		}
		if err := l.storage.SaveAttestation(ctx, attest); err != nil {
			return err
		}
	case bitcom.REVOKE:
		if id == nil {
			return fmt.Errorf("identity not found for address %s", aip.Address)
		} else if attest, err := l.storage.LoadAttestation(ctx, bap.IDKey); err != nil {
			return err
		} else if attest != nil {
			for _, signer := range attest.Signers {
				if signer.Address == aip.Address {
					signer.Revoked = true
					break
				}
			}
			if err := l.storage.SaveAttestation(ctx, attest); err != nil {
				return err
			}
		}

	case bitcom.ALIAS:
		if id == nil {
			return fmt.Errorf("identity not found for address %s", aip.Address)
		}
		if len(bap.Profile) > 0 && bap.IDKey == id.IDKey {
			if err := l.storage.SaveProfile(ctx, bap.IDKey, bap.Profile); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *LookupService) GetDocumentation() string {
	return "Events lookup"
}

func (l *LookupService) GetMetaData() *overlay.MetaData {
	return &overlay.MetaData{
		Name: "Events",
	}
}
