package clientdb

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/llm/order"
	"go.etcd.io/bbolt"
)

var testSnapshot = &LocalBatchSnapshot{
	BatchID:        testBatchID,
	Version:        5,
	ClearingPrice:  999,
	BatchTxFeeRate: 123456,
	BatchTXID:      [32]byte{0, 1, 2, 3, 4, 5},
	Matches: []*Match{
		{
			Nonce:        [32]byte{1, 2, 3},
			UnitsFilled:  19,
			ExecutionFee: 7658,
		},
		{
			Nonce:        [32]byte{1, 2, 3},
			UnitsFilled:  1,
			ExecutionFee: 8,
		},
		{
			Nonce:        [32]byte{9, 0, 9},
			UnitsFilled:  100,
			ExecutionFee: 9999,
		},
	},
}

// TestSerializeLocalBatchSnapshot checks that (de)serialization of local batch
// snapshots worsk as expected.
func TestSerializeLocalBatchSnapshot(t *testing.T) {
	pre := testSnapshot
	buf := bytes.Buffer{}
	if err := serializeLocalBatchSnapshot(&buf, pre); err != nil {
		t.Fatal(err)
	}

	post, err := deserializeLocalBatchSnapshot(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(pre, post) {
		t.Fatalf("mismatch: %v vs %v", spew.Sdump(pre), spew.Sdump(post))
	}
}

// TestStoreLocalBatchSnapshot tests that snapshots stored to the database get
// returned in the same order.
func TestGetLocalBatchSnapshots(t *testing.T) {
	store, cleanup := newTestDB(t)
	defer cleanup()

	// Store the same batch 10 times, only changing the version number each
	// time.
	for i := 0; i < 10; i++ {
		i := i
		err := store.Update(func(tx *bbolt.Tx) error {
			testSnapshot.Version = order.BatchVersion(i)
			return storeLocalBatchSnapshot(tx, testSnapshot)
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Fetch all snapshots and check that they are returned in order.
	snapshots, err := store.GetLocalBatchSnapshots()
	if err != nil {
		t.Fatal(err)
	}

	for i, snapshot := range snapshots {
		testSnapshot.Version = order.BatchVersion(i)
		if !reflect.DeepEqual(testSnapshot, snapshot) {
			t.Fatalf("mismatch: %v vs %v",
				spew.Sdump(testSnapshot), spew.Sdump(snapshot))
		}
	}
}
