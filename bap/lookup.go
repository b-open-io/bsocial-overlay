package bap

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/4chain-ag/go-overlay-services/pkg/core/engine"
	"github.com/b-open-io/overlay/publish"
	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/overlay/lookup"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type LookupService struct {
	db  *mongo.Database
	pub publish.Publisher
}

func NewLookupService(connString string, dbName string, pub publish.Publisher) (*LookupService, error) {
	if client, err := mongo.Connect(nil, options.Client().ApplyURI(connString)); err != nil {
		return nil, err
	} else {
		db := client.Database(dbName)
		return &LookupService{
			db:  db,
			pub: pub,
		}, nil
	}
}

func (l *LookupService) OutputAdmittedByTopic(ctx context.Context, payload *engine.OutputAdmittedByTopic) error {
	// OutputAdded(ctx context.Context, outpoint *transaction.Outpoint, topic string, beef []byte) error {
	_, tx, _, err := transaction.ParseBeef(payload.AtomicBEEF)
	if err != nil {
		return err
	}
	output := tx.Outputs[payload.Outpoint.Index]
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
						Txid:    payload.Outpoint.Txid.String(),
						Block:   height,
					},
				},
				FirstSeen:     height,
				FirstSeenTxid: payload.Outpoint.Txid.String(),
			}
		} else {
			id.CurrentAddress = aip.Address
			id.Addresses = append(id.Addresses, Address{
				Address: bap.Address,
				Txid:    payload.Outpoint.Txid.String(),
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
			Txid:    payload.Outpoint.Txid.String(),
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

func (l *LookupService) OutputSpent(ctx context.Context, payload *engine.OutputSpent) error {
	// Implementation for marking an output as spent
	return nil
}
func (l *LookupService) OutputNoLongerRetainedInHistory(ctx context.Context, outpoint *transaction.Outpoint, topic string) error {
	// Implementation for deleting an output event
	return nil
}
func (l *LookupService) OutputEvicted(ctx context.Context, outpoint *transaction.Outpoint) error {
	// Implementation for evicting an output
	return nil
}
func (l *LookupService) OutputBlockHeightUpdated(ctx context.Context, txid *chainhash.Hash, blockHeight uint32, blockIndex uint64) error {
	// Implementation for updating the block height of an output
	return nil
}
func (l *LookupService) Lookup(ctx context.Context, question *lookup.LookupQuestion) (*lookup.LookupAnswer, error) {
	// Implementation for looking up events based on the question
	return nil, nil
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
	err := l.db.Collection("identities").FindOne(ctx, bson.M{"_id": id}).Decode(identity)
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
	p := bson.M{}
	if err := json.Unmarshal(profile, &p); err != nil {
		return fmt.Errorf("failed to unmarshal profile: %w", err)
	}
	_, err := l.db.Collection("identities").UpdateOne(ctx,
		bson.M{"_id": bapId},
		bson.M{"$set": bson.M{"profile": p}},
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

func (l *LookupService) Search(ctx context.Context, query string, limit int, offset int) ([]*Identity, error) {
	// Implementation for searching identities based on a query using Atlas Search $search aggregation
	pipeline := mongo.Pipeline{
		{ // First stage: $search
			bson.E{Key: "$search", Value: bson.D{
				{Key: "index", Value: "default"}, // Replace with your Atlas Search index name if different
				{Key: "text", Value: bson.D{
					{Key: "query", Value: query},                                // Use the input query, wrapped with wildcards
					{Key: "path", Value: bson.D{{Key: "wildcard", Value: "*"}}}, // Search across all fields
					// {Key: "allowAnalyzedField", Value: true},                    // Optional: Can improve performance if fields are analyzed
				}},
			}},
		},
		{ // Second stage: $skip
			bson.E{Key: "$skip", Value: int64(offset)}, // Skip the specified number of documents
		},
		{ // Third stage: $limit
			bson.E{Key: "$limit", Value: int64(limit)}, // Limit the number of results
		},
		// Optional: Add other stages like $project if needed
	}

	cursor, err := l.db.Collection("identities").Aggregate(ctx, pipeline)
	if err != nil {
		// Log the pipeline for debugging if needed
		// log.Printf("Search pipeline: %+v\n", pipeline)
		return nil, fmt.Errorf("failed to execute search aggregation: %w", err)
	}
	defer cursor.Close(ctx)

	var identities []*Identity
	if err = cursor.All(ctx, &identities); err != nil {
		return nil, fmt.Errorf("failed to decode search results: %w", err)
	}

	return identities, nil
}
