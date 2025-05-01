package bap

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/b-open-io/overlay/storage"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/redis/go-redis/v9"
)

type BAPStorage struct {
	*storage.RedisStorage
}

func (s *BAPStorage) LoadIdentityById(ctx context.Context, id string) (*Identity, error) {
	if b, err := s.DB.HGet(ctx, IdentityKey, id).Bytes(); err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else {
		var identity Identity
		if err := json.Unmarshal(b, &identity); err != nil {
			return nil, err
		}
		return &identity, nil
	}
}

func (s *BAPStorage) LoadIdentityByAddress(ctx context.Context, address string) (*Identity, error) {
	if idkey, err := s.DB.HGet(ctx, AddressIdentityKey, address).Result(); err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else if b, err := s.DB.HGet(ctx, IdentityKey, idkey).Bytes(); err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else {
		var identity Identity
		if err := json.Unmarshal(b, &identity); err != nil {
			return nil, err
		}
		return &identity, nil
	}
}

func (s *BAPStorage) PopulateAddressHeights(ctx context.Context, id *Identity) error {
	if id == nil {
		return nil
	}
	// updated := false
	for _, addr := range id.Addresses {
		if addr.Block == 0 {
			if beefBytes, err := s.TxStore.LoadBeef(ctx, &addr.Txid); err != nil {
				return err
			} else if len(beefBytes) == 0 {
				continue
			} else if tx, err := transaction.NewTransactionFromBEEF(beefBytes); err != nil {
				return err
			} else if tx.MerklePath != nil {
				addr.Block = tx.MerklePath.BlockHeight
				// updated = true
			}
		}
	}
	// if updated {
	// 	s.SaveIdentity(ctx, id)
	// }
	return nil
}

func (s *BAPStorage) SaveIdentity(ctx context.Context, id *Identity) error {
	if b, err := json.Marshal(id); err != nil {
		return err
	} else {
		_, err := s.DB.Pipelined(ctx, func(p redis.Pipeliner) error {
			if err := p.HSet(ctx, IdentityKey, id.IDKey, b).Err(); err != nil {
				return err
			} else if err := p.HSet(ctx, AddressIdentityKey, id.CurrentAddress, id.IDKey).Err(); err != nil {
				return err
			}

			return nil
		})
		return err
	}
}

func (s *BAPStorage) LoadAttestation(ctx context.Context, urnHash string) (*Attestation, error) {
	if b, err := s.DB.HGet(ctx, AttestationKey, urnHash).Bytes(); err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else {
		var att Attestation
		if err := json.Unmarshal(b, &att); err != nil {
			return nil, err
		}
		return &att, nil
	}
}

func (s *BAPStorage) SaveAttestation(ctx context.Context, att *Attestation) error {
	if b, err := json.Marshal(att); err != nil {
		return err
	} else {
		return s.DB.HSet(ctx, AttestationKey, att.Id, b).Err()
	}
}

func (s *BAPStorage) LoadProfile(ctx context.Context, bapId string) (json.RawMessage, error) {
	if msg, err := s.DB.HGet(ctx, ProfileKey, bapId).Bytes(); err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else {
		return msg, nil
	}
}

func (s *BAPStorage) SaveProfile(ctx context.Context, bapId string, profile json.RawMessage) error {
	if p, err := json.Marshal(profile); err != nil {
		log.Panicln("Failed to marshal profile:", bapId, string(profile), err)
		return err
	} else {
		_, err := s.DB.Pipelined(ctx, func(pipe redis.Pipeliner) error {
			if err := pipe.HSet(ctx, ProfileKey, bapId, string(p)).Err(); err != nil {
				return err
			} else if pipe.ZAdd(ctx, ProfileSeqKey, redis.Z{
				Score:  float64(time.Now().UnixNano()),
				Member: bapId,
			}).Err() != nil {
				return err
			}
			return nil
		})
		return err
	}
}

func (s *BAPStorage) LoadProfiles(ctx context.Context, limit int, offset int) (profiles []*Profile, err error) {
	if ids, err := s.DB.ZRange(ctx, ProfileSeqKey, int64(offset), int64(limit+offset-1)).Result(); err != nil {
		return nil, err
	} else if len(ids) == 0 {
		return nil, nil
	} else if data, err := s.DB.HMGet(ctx, ProfileKey, ids...).Result(); err != nil {
		return nil, err
	} else {
		profiles = make([]*Profile, len(ids))
		for i, id := range ids {
			if data[i] == nil {
				continue
			}
			profile := &Profile{
				IDKey: id,
				Data:  json.RawMessage(data[i].(string)),
			}
			profiles[i] = profile
		}
		return profiles, nil
	}
}
