package bap

// type BAPStorage struct {
// 	*storage.MongoStorage
// }

// func (s *BAPStorage) LoadIdentityById(ctx context.Context, id string) (*Identity, error) {
// 	identity := &Identity{}
// 	err := s.DB.Collection("identities").FindOne(ctx, bson.M{"id": id}).Decode(identity)
// 	if err == mongo.ErrNoDocuments {
// 		return nil, nil
// 	} else if err != nil {
// 		return nil, err
// 	}
// 	return identity, nil
// }

// func (s *BAPStorage) LoadIdentityByAddress(ctx context.Context, address string) (*Identity, error) {
// 	identity := &Identity{}
// 	err := s.DB.Collection("identities").FindOne(ctx, bson.M{"addresses.address": address}).Decode(identity)
// 	if err == mongo.ErrNoDocuments {
// 		return nil, nil
// 	} else if err != nil {
// 		return nil, err
// 	}
// 	return identity, nil
// }

// func (s *BAPStorage) PopulateAddressHeights(ctx context.Context, id *Identity) error {
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

// func (s *BAPStorage) SaveIdentity(ctx context.Context, id *Identity) error {
// 	_, err := s.DB.Collection("identities").UpdateOne(ctx,
// 		bson.M{"_id": id.BapId},
// 		bson.M{"$set": id},
// 		options.UpdateOne().SetUpsert(true),
// 	)
// 	return err
// }

// func (s *BAPStorage) LoadAttestation(ctx context.Context, urnHash string) (*Attestation, error) {
// 	// attestation := &Attestation{}
// 	if cursor, err := s.DB.Collection("attestations").Find(ctx, bson.M{"urnHash": urnHash}); err != nil {
// 		return nil, err
// 	} else {
// 		defer cursor.Close(ctx)
// 		attestation := &Attestation{
// 			UrnHash: urnHash,
// 		}
// 		for cursor.Next(ctx) {
// 			signer := &Signer{}
// 			if err := cursor.Decode(signer); err != nil {
// 				log.Println("Failed to decode signer:", err)
// 				return nil, err
// 			}
// 			attestation.Signers = append(attestation.Signers, signer)
// 		}
// 		if err := cursor.Err(); err != nil {
// 			return nil, err
// 		}
// 		return attestation, nil
// 	}
// }

// func (s *BAPStorage) SaveAttestation(ctx context.Context, signer *Signer) error {
// 	_, err := s.DB.Collection("attestations").UpdateOne(ctx,
// 		bson.M{"urnHash": signer.UrnHash, "idKey": signer.BapID},
// 		bson.M{"$set": signer},
// 		options.UpdateOne().SetUpsert(true),
// 	)
// 	return err
// }

// func (s *BAPStorage) RevokeAttestation(ctx context.Context, bapId string, urnHash string) error {
// 	_, err := s.DB.Collection("attestations").UpdateOne(ctx,
// 		bson.M{"urnHash": urnHash, "idKey": bapId},
// 		bson.M{"$set": bson.M{"revoked": true}},
// 		options.UpdateOne().SetUpsert(true),
// 	)
// 	return err
// }

// func (s *BAPStorage) DeleteAttestation(ctx context.Context, signer *Signer) error {
// 	_, err := s.DB.Collection("attestations").DeleteOne(ctx,
// 		bson.M{"urnHash": signer.UrnHash, "idKey": signer.BapID},
// 	)
// 	return err
// }

// func (s *BAPStorage) SaveProfile(ctx context.Context, bapId string, profile json.RawMessage) error {
// 	_, err := s.DB.Collection("identities").UpdateOne(ctx,
// 		bson.M{"_id": bapId},
// 		bson.M{"$set": bson.M{"profile": profile}},
// 	)
// 	return err
// }

// func (s *BAPStorage) LoadProfiles(ctx context.Context, limit int, offset int) (profiles []*Profile, err error) {
// 	if cursor, err := s.DB.Collection("identities").Find(
// 		ctx,
// 		bson.M{},
// 		options.Find().
// 			SetLimit(int64(limit)).
// 			SetSkip(int64(offset)).
// 			SetProjection(bson.M{
// 				"_id":     1,
// 				"profile": 1,
// 			}),
// 	); err != nil {
// 		return nil, err
// 	} else {
// 		defer cursor.Close(ctx)
// 		profiles = make([]*Profile, 0, limit)
// 		for cursor.Next(ctx) {
// 			var profile Profile
// 			if err := cursor.Decode(&profile); err != nil {
// 				log.Println("Failed to decode profile:", err)
// 				continue
// 			}
// 			profiles = append(profiles, &profile)
// 		}
// 		if err := cursor.Err(); err != nil {
// 			return nil, err
// 		}
// 		return profiles, nil
// 	}
// }
