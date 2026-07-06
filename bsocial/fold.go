package bsocial

import (
	"math"
	"sort"
)

type FollowEvent struct {
	Key       string // idKey (following) or address (followers)
	Txid      string
	Height    uint32 // 0 => unconfirmed
	Timestamp int64  // ingest ms, tie-break
	Unfollow  bool
}

// FoldFollows returns the surviving follow events per Key.
// Rules: per Key, the event with the highest (effective height, timestamp)
// wins; effective height for Height==0 (unconfirmed) is math.MaxUint32.
// On exact ties, unfollow wins (conservative). Surviving unfollows are
// dropped from the result.
func FoldFollows(events []FollowEvent) []FollowEvent {
	latest := make(map[string]FollowEvent)
	for _, event := range events {
		if event.Key == "" {
			continue
		}
		current, ok := latest[event.Key]
		if !ok || followEventWins(event, current) {
			latest[event.Key] = event
		}
	}

	result := make([]FollowEvent, 0, len(latest))
	for _, event := range latest {
		if !event.Unfollow {
			result = append(result, event)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})

	return result
}

func followEventWins(candidate FollowEvent, current FollowEvent) bool {
	candidateHeight := effectiveFollowHeight(candidate.Height)
	currentHeight := effectiveFollowHeight(current.Height)
	if candidateHeight != currentHeight {
		return candidateHeight > currentHeight
	}
	if candidate.Timestamp != current.Timestamp {
		return candidate.Timestamp > current.Timestamp
	}
	return candidate.Unfollow && !current.Unfollow
}

func effectiveFollowHeight(height uint32) uint32 {
	if height == 0 {
		return math.MaxUint32
	}
	return height
}
