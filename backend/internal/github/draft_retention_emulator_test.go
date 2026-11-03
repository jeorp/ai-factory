package github

import (
	"cloud.google.com/go/firestore"
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestEmulatorDraftRenewalIsAtomicAndPreservesContent(t *testing.T) {
	if os.Getenv("RUN_EMULATOR_TESTS") != "1" || os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	db, err := firestore.NewClient(ctx, "demo-ai-factory")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &FirestoreReadCache{DB: db}
	key := fmt.Sprintf("draft-retention-test-%d", time.Now().UnixNano())
	keys := []string{key + "-live", key + "-expired"}
	// Delete just these two test-owned cache entries before closing the client.
	defer func() {
		for _, k := range keys {
			db.Collection("githubReadCache").Doc(k).Delete(ctx)
		}
	}()
	originalExpiry := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	for i, k := range keys {
		expiry := originalExpiry
		if i == 1 {
			expiry = time.Now().Add(-time.Hour)
		}
		if _, err := db.Collection("githubReadCache").Doc(k).Set(ctx, cacheEntry{Content: "current", ExpiresAt: expiry}); err != nil {
			t.Fatal(err)
		}
	}
	until := time.Now().Add(DraftTTL).Truncate(time.Microsecond)
	if err := c.Extend(ctx, keys, until); err == nil {
		t.Fatal("expired draft resurrected")
	}
	snap, err := db.Collection("githubReadCache").Doc(keys[0]).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var unchanged cacheEntry
	if snap.DataTo(&unchanged) != nil || !unchanged.ExpiresAt.Equal(originalExpiry) {
		t.Fatal("failed renewal changed live file expiry")
	}
	if _, err := db.Collection("githubReadCache").Doc(keys[1]).Set(ctx, cacheEntry{Content: "new edit", ExpiresAt: originalExpiry}); err != nil {
		t.Fatal(err)
	}
	if err := c.Extend(ctx, keys, until); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		snap, err := db.Collection("githubReadCache").Doc(k).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var got cacheEntry
		if snap.DataTo(&got) != nil || !got.ExpiresAt.Equal(until) {
			t.Fatalf("expiry not extended: %+v", got)
		}
		want := "current"
		if i == 1 {
			want = "new edit"
		}
		if got.Content != want {
			t.Fatal("new content overwritten")
		}
	}
}
