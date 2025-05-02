package bap

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/overlay/lookup"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type LookupService struct {
	db *mongo.Database
}

func NewLookupService(connString string, dbName string) (*LookupService, error) {
	if client, err := mongo.Connect(nil, options.Client().ApplyURI(connString)); err != nil {
		return nil, err
	} else {
		db := client.Database(dbName)
		return &LookupService{
			db: db,
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
	id, err := l.LoadIdentityByAddress(ctx, aip.Address)
	if err != nil {
		return err
	}
	switch bap.Type {
	case bitcom.ID:
		if id == nil {
			id = &Identity{
				BapId:          bap.IDKey,
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
		if err := l.SaveIdentity(ctx, id); err != nil {
			return err
		}
	case bitcom.ATTEST:
		if id == nil {
			return fmt.Errorf("identity not found for address %s", aip.Address)
		}
		signer := &Signer{
			BapID:   id.BapId,
			UrnHash: bap.IDKey,
			Address: aip.Address,
			Txid:    outpoint.Txid,
			Revoked: false,
		}

		if err := l.SaveAttestation(ctx, signer); err != nil {
			return err
		}
	case bitcom.REVOKE:
		if err := l.RevokeAttestation(ctx, id.BapId, bap.IDKey); err != nil {
			return err
		}

	case bitcom.ALIAS:
		if id == nil {
			return fmt.Errorf("identity not found for address %s", aip.Address)
		}
		if len(bap.Profile) > 0 && bap.IDKey == id.BapId {
			if err := l.SaveProfile(ctx, bap.IDKey, bap.Profile); err != nil {
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

func (l *LookupService) LoadIdentityById(ctx context.Context, id string) (*Identity, error) {
	identity := &Identity{}
	err := l.db.Collection("identities").FindOne(ctx, bson.M{"id": id}).Decode(identity)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return identity, nil
}

func (l *LookupService) LoadIdentityByAddress(ctx context.Context, address string) (*Identity, error) {
	identity := &Identity{}
	err := l.db.Collection("identities").FindOne(ctx, bson.M{"addresses.address": address}).Decode(identity)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return identity, nil
}

// func (l *LookupService) PopulateAddressHeights(ctx context.Context, id *Identity) error {
// 	if id == nil {
// 		return nil
// 	}
// 	// updated := false
// 	for _, addr := range id.Addresses {
// 		if addr.Block == 0 {
// 			if beefBytes, err := s.BeefStore.LoadBeef(ctx, &addr.Txid); err != nil {
// 				return err
// 			} else if len(beefBytes) == 0 {
// 				continue
// 			} else if tx, err := transaction.NewTransactionFromBEEF(beefBytes); err != nil {
// 				return err
// 			} else if tx.MerklePath != nil {
// 				addr.Block = tx.MerklePath.BlockHeight
// 			}
// 		}
// 	}
// 	// if updated {
// 	// 	s.SaveIdentity(ctx, id)
// 	// }
// 	return nil
// }

func (l *LookupService) SaveIdentity(ctx context.Context, id *Identity) error {
	_, err := l.db.Collection("identities").UpdateOne(ctx,
		bson.M{"_id": id.BapId},
		bson.M{"$set": id},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

func (l *LookupService) LoadAttestation(ctx context.Context, urnHash string) (*Attestation, error) {
	// attestation := &Attestation{}
	if cursor, err := l.db.Collection("attestations").Find(ctx, bson.M{"urnHash": urnHash}); err != nil {
		return nil, err
	} else {
		defer cursor.Close(ctx)
		attestation := &Attestation{
			UrnHash: urnHash,
		}
		for cursor.Next(ctx) {
			signer := &Signer{}
			if err := cursor.Decode(signer); err != nil {
				log.Println("Failed to decode signer:", err)
				return nil, err
			}
			attestation.Signers = append(attestation.Signers, signer)
		}
		if err := cursor.Err(); err != nil {
			return nil, err
		}
		return attestation, nil
	}
}

func (l *LookupService) SaveAttestation(ctx context.Context, signer *Signer) error {
	_, err := l.db.Collection("attestations").UpdateOne(ctx,
		bson.M{"urnHash": signer.UrnHash, "idKey": signer.BapID},
		bson.M{"$set": signer},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

func (l *LookupService) RevokeAttestation(ctx context.Context, bapId string, urnHash string) error {
	_, err := l.db.Collection("attestations").UpdateOne(ctx,
		bson.M{"urnHash": urnHash, "idKey": bapId},
		bson.M{"$set": bson.M{"revoked": true}},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

func (l *LookupService) DeleteAttestation(ctx context.Context, signer *Signer) error {
	_, err := l.db.Collection("attestations").DeleteOne(ctx,
		bson.M{"urnHash": signer.UrnHash, "idKey": signer.BapID},
	)
	return err
}

func (l *LookupService) SaveProfile(ctx context.Context, bapId string, profile json.RawMessage) error {
	_, err := l.db.Collection("identities").UpdateOne(ctx,
		bson.M{"_id": bapId},
		bson.M{"$set": bson.M{"profile": profile}},
	)
	return err
}

func (l *LookupService) LoadProfiles(ctx context.Context, limit int, offset int) (profiles []*Profile, err error) {
	if cursor, err := l.db.Collection("identities").Find(
		ctx,
		bson.M{},
		options.Find().
			SetLimit(int64(limit)).
			SetSkip(int64(offset)).
			SetProjection(bson.M{
				"_id":     1,
				"profile": 1,
			}),
	); err != nil {
		return nil, err
	} else {
		defer cursor.Close(ctx)
		profiles = make([]*Profile, 0, limit)
		for cursor.Next(ctx) {
			var profile Profile
			if err := cursor.Decode(&profile); err != nil {
				log.Println("Failed to decode profile:", err)
				continue
			}
			profiles = append(profiles, &profile)
		}
		if err := cursor.Err(); err != nil {
			return nil, err
		}
		return profiles, nil
	}
}

func (l *LookupService) OutputSpent(ctx context.Context, outpoint *overlay.Outpoint, topic string, beef []byte) error {
	// Implementation for marking an output as spent
	return nil
}
func (l *LookupService) OutputDeleted(ctx context.Context, outpoint *overlay.Outpoint, topic string) error {
	// Implementation for deleting an output event
	return nil
}
func (l *LookupService) OutputBlockHeightUpdated(ctx context.Context, outpoint *overlay.Outpoint, blockHeight uint32, blockIndex uint64) error {
	// Implementation for updating the block height of an output
	return nil
}
func (l *LookupService) Lookup(ctx context.Context, question *lookup.LookupQuestion) (*lookup.LookupAnswer, error) {
	// Implementation for looking up events based on the question
	return nil, nil
}
