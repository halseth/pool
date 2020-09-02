package clientdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/llm/order"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"go.etcd.io/bbolt"
)

// batch-snapshot-bucket
//         |
//         |-- <sequence num>
//         |        |
//         |        |-- batch-snapshot-batch: <batch>
//         |
//         |-- <sequence num>
//         |        |
//        ...      ...
//
var (
	// batchSnapshotBucketKey is the top level bucket where we'll find
	// snapshot information about all batches we have participated in.
	batchSnapshotBucketKey = []byte("batch-snapshot-bucket")

	// batchSnapshotBatchKey is the key under where we'll store the
	// serialized batch.
	batchSnapshotBatchKey = []byte("batch-snapshot-batch")
)

// Match is a snapshot of a match made in the batch.
type Match struct {
	// Nonce is the nonce of the local order that was part of the match.
	Nonce order.Nonce

	// UnitsFilled is the units that got filled by this match.
	UnitsFilled order.SupplyUnit

	// ExecutionFee is the fee in sats paid to the auctioneer.
	ExecutionFee btcutil.Amount
}

// LocalBatchSnapshot holds key information about our participation in a batch.
type LocalBatchSnapshot struct {
	// BatchID is the batch's unique ID.
	BatchID order.BatchID

	// BatchVersion is the version of the batch verification protocol.
	Version order.BatchVersion

	// ClearingPrice is the fixed rate the orders were cleared at.
	ClearingPrice order.FixedRatePremium

	// BatchTxFeeRate is the miner fee rate in sat/kW that was chosen for
	// the batch transaction.
	BatchTxFeeRate chainfee.SatPerKWeight

	// BatchTXID is the transaction hash of the batch tx
	BatchTXID chainhash.Hash

	// Matches holds snapshots of matches we were involved in in this
	// batch.
	Matches []*Match
}

// NewSnapshots creates a new LocalBatchSnapshot from the passed order batched.
func NewSnapshot(batch *order.Batch) *LocalBatchSnapshot {
	feeSched := batch.ExecutionFee
	snapshot := &LocalBatchSnapshot{
		BatchID:        batch.ID,
		Version:        batch.Version,
		ClearingPrice:  batch.ClearingPrice,
		BatchTxFeeRate: batch.BatchTxFeeRate,
		BatchTXID:      batch.BatchTX.TxHash(),
	}

	for nonce, theirOrders := range batch.MatchedOrders {

		// For each order we matched with, take note of the units
		// filled and the execution fee paid.
		for _, theirOrder := range theirOrders {
			amt := theirOrder.UnitsFilled.ToSatoshis()
			exeFee := feeSched.BaseFee() + feeSched.ExecutionFee(amt)

			snapshot.Matches = append(snapshot.Matches, &Match{
				Nonce:        nonce,
				UnitsFilled:  theirOrder.UnitsFilled,
				ExecutionFee: exeFee,
			})
		}
	}

	return snapshot
}

// GetLocalBatchSnapshots returns snapshots for all batches the trader has
// participated in.
func (db *DB) GetLocalBatchSnapshots() ([]*LocalBatchSnapshot, error) {
	var snapshots []*LocalBatchSnapshot
	err := db.View(func(tx *bbolt.Tx) error {
		var err error
		snapshots, err = db.fetchLocalBatchSnapshots(tx)
		return err
	})
	if err != nil {
		return nil, err
	}

	return snapshots, nil
}

func (db *DB) fetchLocalBatchSnapshots(tx *bbolt.Tx) ([]*LocalBatchSnapshot,
	error) {

	bucket, err := getBucket(tx, batchSnapshotBucketKey)
	if err != nil {
		return nil, err
	}

	// Each entry in the top-level bucket is a sub-bucket index by the
	// sequence number.
	var snapshots []*LocalBatchSnapshot
	err = bucket.ForEach(func(seq, v []byte) error {
		snapshotBucket, err := getNestedBucket(bucket, seq, false)
		if err != nil {
			return err
		}

		batchSnapshot, err := fetchLocalBatchSnapshot(snapshotBucket)
		if err != nil {
			return err
		}

		// Add this batch to our list of snapshots.
		snapshots = append(snapshots, batchSnapshot)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return snapshots, nil
}

func fetchLocalBatchSnapshot(snapshotBucket *bbolt.Bucket) (*LocalBatchSnapshot,
	error) {

	// Get the serialized batch.
	rawBatch := snapshotBucket.Get(batchSnapshotBatchKey)
	if rawBatch == nil {
		return nil, fmt.Errorf("batch not found for snapshot")
	}

	return deserializeLocalBatchSnapshot(bytes.NewReader(rawBatch))
}

func storeLocalBatchSnapshot(tx *bbolt.Tx, batch *LocalBatchSnapshot) error {
	bucket, err := getBucket(tx, batchSnapshotBucketKey)
	if err != nil {
		return err
	}

	// Get the next sequence number we will store this batch under.
	sequence, err := bucket.NextSequence()
	if err != nil {
		return err
	}
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], sequence)

	// Create a sub-bucket for this sequence number.
	snapshotBucket, err := getNestedBucket(bucket, seqBytes[:], true)
	if err != nil {
		return err
	}

	buf := bytes.Buffer{}
	if err := serializeLocalBatchSnapshot(&buf, batch); err != nil {
		return err
	}

	err = snapshotBucket.Put(batchSnapshotBatchKey, buf.Bytes())
	if err != nil {
		return err
	}

	return nil
}

func serializeLocalBatchSnapshot(w io.Writer, b *LocalBatchSnapshot) error {
	err := WriteElements(
		w, b.BatchID[:], uint32(b.Version), b.ClearingPrice,
		b.BatchTxFeeRate, b.BatchTXID[:],
	)
	if err != nil {
		return err
	}

	numMatches := uint32(len(b.Matches))
	err = WriteElements(w, numMatches)
	if err != nil {
		return err
	}

	for _, m := range b.Matches {
		err := serializeMatch(w, m)
		if err != nil {
			return err
		}
	}

	return nil
}

func deserializeLocalBatchSnapshot(r io.Reader) (*LocalBatchSnapshot, error) {
	b := &LocalBatchSnapshot{}
	var version uint32
	err := ReadElements(
		r, b.BatchID[:], &version, &b.ClearingPrice,
		&b.BatchTxFeeRate, b.BatchTXID[:],
	)
	if err != nil {
		return nil, err
	}

	b.Version = order.BatchVersion(version)

	var numMatchedOrders uint32
	err = ReadElements(r, &numMatchedOrders)
	if err != nil {
		return nil, err
	}

	for i := uint32(0); i < numMatchedOrders; i++ {
		m, err := deserializeMatch(r)
		if err != nil {
			return nil, err
		}

		b.Matches = append(b.Matches, m)
	}

	return b, nil
}

func serializeMatch(w io.Writer, m *Match) error {
	return WriteElements(w, m.Nonce, m.UnitsFilled, m.ExecutionFee)
}

func deserializeMatch(r io.Reader) (*Match, error) {
	m := &Match{}
	err := ReadElements(r, &m.Nonce, &m.UnitsFilled, &m.ExecutionFee)
	if err != nil {
		return nil, err
	}

	return m, nil
}
