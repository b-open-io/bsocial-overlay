package bsocial

import "testing"

func TestFoldFollowsFollowOnlySurvives(t *testing.T) {
	result := FoldFollows([]FollowEvent{
		{Key: "alice", Txid: "tx1", Height: 10, Timestamp: 100},
	})

	if len(result) != 1 {
		t.Fatalf("expected 1 surviving follow, got %d", len(result))
	}
	if result[0].Key != "alice" || result[0].Txid != "tx1" || result[0].Height != 10 {
		t.Fatalf("unexpected surviving follow: %+v", result[0])
	}
}

func TestFoldFollowsUnfollowAtHigherHeightRemovesFollow(t *testing.T) {
	result := FoldFollows([]FollowEvent{
		{Key: "alice", Txid: "tx1", Height: 10, Timestamp: 100},
		{Key: "alice", Txid: "tx2", Height: 20, Timestamp: 200, Unfollow: true},
	})

	if len(result) != 0 {
		t.Fatalf("expected no surviving follows, got %+v", result)
	}
}

func TestFoldFollowsRefollowAtHigherHeightSurvives(t *testing.T) {
	result := FoldFollows([]FollowEvent{
		{Key: "alice", Txid: "tx1", Height: 10, Timestamp: 100},
		{Key: "alice", Txid: "tx2", Height: 20, Timestamp: 200, Unfollow: true},
		{Key: "alice", Txid: "tx3", Height: 30, Timestamp: 300},
	})

	if len(result) != 1 {
		t.Fatalf("expected 1 surviving follow, got %d", len(result))
	}
	if result[0].Txid != "tx3" || result[0].Height != 30 {
		t.Fatalf("expected re-follow at height 30, got %+v", result[0])
	}
}

func TestFoldFollowsUnconfirmedFollowAfterConfirmedUnfollowSurvives(t *testing.T) {
	result := FoldFollows([]FollowEvent{
		{Key: "alice", Txid: "tx1", Height: 10, Timestamp: 100},
		{Key: "alice", Txid: "tx2", Height: 20, Timestamp: 200, Unfollow: true},
		{Key: "alice", Txid: "tx3", Height: 0, Timestamp: 300},
	})

	if len(result) != 1 {
		t.Fatalf("expected 1 surviving follow, got %d", len(result))
	}
	if result[0].Txid != "tx3" || result[0].Height != 0 {
		t.Fatalf("expected unconfirmed follow to survive, got %+v", result[0])
	}
}

func TestFoldFollowsExactTieUnfollowWins(t *testing.T) {
	result := FoldFollows([]FollowEvent{
		{Key: "alice", Txid: "tx1", Height: 10, Timestamp: 100},
		{Key: "alice", Txid: "tx2", Height: 10, Timestamp: 100, Unfollow: true},
	})

	if len(result) != 0 {
		t.Fatalf("expected exact-tie unfollow to remove follow, got %+v", result)
	}
}

func TestFoldFollowsMultipleKeysFoldedIndependently(t *testing.T) {
	result := FoldFollows([]FollowEvent{
		{Key: "alice", Txid: "tx1", Height: 10, Timestamp: 100},
		{Key: "bob", Txid: "tx2", Height: 10, Timestamp: 100},
		{Key: "alice", Txid: "tx3", Height: 20, Timestamp: 200, Unfollow: true},
		{Key: "carol", Txid: "tx4", Height: 30, Timestamp: 300},
	})

	if len(result) != 2 {
		t.Fatalf("expected 2 surviving follows, got %+v", result)
	}
	if result[0].Key != "bob" || result[0].Txid != "tx2" {
		t.Fatalf("expected bob first after key sort, got %+v", result[0])
	}
	if result[1].Key != "carol" || result[1].Txid != "tx4" {
		t.Fatalf("expected carol second after key sort, got %+v", result[1])
	}
}
