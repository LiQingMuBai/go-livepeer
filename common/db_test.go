package common

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"math"
	"math/big"
	"testing"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/livepeer/go-livepeer/pm"
	"github.com/livepeer/lpms/ffmpeg"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
)

func TestDBLastSeenBlock(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	if err != nil {
		return
	}
	defer dbh.Close()
	defer dbraw.Close()

	// sanity check default value
	var val int64
	var created_at string
	var updated_at string
	stmt := "SELECT value, updatedAt FROM kv WHERE key = 'lastBlock'"
	row := dbraw.QueryRow(stmt)
	err = row.Scan(&val, &created_at)
	if err != nil || val != int64(0) {
		t.Errorf("Unexpected result from sanity check; got %v - %v", err, val)
		return
	}
	// set last updated at timestamp to sometime in the past
	update_stmt := "UPDATE kv SET updatedAt = datetime('now', '-2 months') WHERE key = 'lastBlock'"
	_, err = dbraw.Exec(update_stmt) // really should sanity check this result
	if err != nil {
		t.Error("Could not update db ", err)
	}

	// now test set
	blkval := int64(54321)
	err = dbh.SetLastSeenBlock(big.NewInt(blkval))
	if err != nil {
		t.Error("Unable to set last seen block ", err)
		return
	}
	row = dbraw.QueryRow(stmt)
	err = row.Scan(&val, &updated_at)
	if err != nil || val != blkval {
		t.Errorf("Unexpected result from value check; got %v - %v", err, val)
		return
	}
	// small possibility of a test failure if we executed over a 1s boundary
	if updated_at != created_at {
		t.Errorf("Unexpected result from update check; got %v:%v", updated_at, created_at)
		return
	}

	// test getter function
	blk, err := dbh.LastSeenBlock()
	if err != nil {
		t.Error(err)
		return
	}
	if blk.Int64() != blkval {
		t.Errorf("Unexpected result from getter; expected %v, got %v", blkval, blk.Int64())
		return
	}
}

func TestDBVersion(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	if err != nil {
		return
	}
	defer dbh.Close()
	defer dbraw.Close()

	// sanity check db version matches
	var dbVersion int
	row := dbraw.QueryRow("SELECT value FROM kv WHERE key = 'dbVersion'")
	err = row.Scan(&dbVersion)
	if err != nil || dbVersion != LivepeerDBVersion {
		t.Errorf("Unexpected result from sanity check; got %v - %v", err, dbVersion)
		return
	}

	// ensure error when db version > current node version
	stmt := fmt.Sprintf("UPDATE kv SET value='%v' WHERE key='dbVersion'", LivepeerDBVersion+1)
	_, err = dbraw.Exec(stmt)
	if err != nil {
		t.Error("Could not update dbversion", err)
		return
	}
	dbh2, err := InitDB(dbPath(t))
	if err == nil || err != ErrDBTooNew {
		t.Error("Did not get expected error DBTooNew; got ", err)
		return
	}
	if dbh2 != nil {
		dbh.Close()
	}
}

func NewStubJob() *DBJob {
	return &DBJob{
		ID: 0, streamID: "1", price: 0,
		profiles: []ffmpeg.VideoProfile{
			ffmpeg.P720p60fps16x9,
			ffmpeg.P360p30fps4x3,
			ffmpeg.P144p30fps16x9,
		}, broadcaster: ethcommon.Address{}, Transcoder: ethcommon.Address{},
		startBlock: 1, endBlock: 2,
	}
}

func profilesMatch(j1 []ffmpeg.VideoProfile, j2 []ffmpeg.VideoProfile) bool {
	if len(j1) != len(j2) {
		return false
	}
	for i, v := range j1 {
		if j2[i] != v {
			return false
		}
	}
	return true
}

func TestDBJobs(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	j := NewStubJob()
	dbh.InsertJob(j)
	j.ID = 1
	dbh.InsertJob(j)
	endBlock := j.endBlock
	j.ID = 2
	j.endBlock += 5
	dbh.InsertJob(j)
	jobs, err := dbh.ActiveJobs(big.NewInt(0))
	if err != nil || len(jobs) != 3 {
		t.Error("Unexpected error in active jobs ", err, len(jobs))
		return
	}
	jobs, err = dbh.ActiveJobs(big.NewInt(endBlock))
	if err != nil || len(jobs) != 1 || jobs[0].ID != 2 {
		t.Error("Unexpected error in active jobs ", err, len(jobs))
		return
	}
	if !profilesMatch(jobs[0].profiles, j.profiles) {
		t.Error("Mismatched profiles in query")
	}
	// check stop reason filter
	dbh.SetStopReason(big.NewInt(j.ID), "insufficient lolz")
	jobs, err = dbh.ActiveJobs(big.NewInt(0))
	if err != nil || len(jobs) != 2 {
		t.Error("Unexpected error in active jobs ", err, len(jobs))
	}

	// test getting a job
	dbjob, err := dbh.GetJob(j.ID)
	if err != nil {
		t.Error("Unexpected error when fetching job ", err)
	}
	if dbjob.ID != j.ID || dbjob.StopReason.String != "insufficient lolz" ||
		!profilesMatch(dbjob.profiles, j.profiles) ||
		dbjob.Transcoder != j.Transcoder ||
		dbjob.broadcaster != j.broadcaster ||
		dbjob.startBlock != j.startBlock || dbjob.endBlock != j.endBlock {
		t.Error("Job mismatch ")
	}
	// should be a nonexistent job
	dbjob, err = dbh.GetJob(100)
	if err != sql.ErrNoRows {
		t.Error("Missing error or unexpected error", err)
	}
	// job with a null stop reason
	dbjob, err = dbh.GetJob(1)
	if err != nil {
		t.Error("Unexpected error ", err)
	}
	if dbjob.StopReason.Valid {
		t.Error("Unexpected stop reason ", dbjob.StopReason.String)
	}

	// should have an invalid profile
	dbraw.Exec("UPDATE jobs SET transcodeOptions = 'invalid' WHERE id = 1")
	_, err = dbh.GetJob(1)
	if err != ErrProfile {
		t.Error("Unexpected result from invalid profile ", err)
	}
}

func TestDBReceipts(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	jid := big.NewInt(0)
	ir := func(j *big.Int, seq int) error {
		b := []byte("")
		n := time.Now()
		return dbh.InsertReceipt(j, int64(seq), "", b, b, b, n, n)
	}
	err = ir(jid, 1)
	if err == nil {
		t.Error("Expected foreign key constraint to fail; nonexistent job")
		return
	}
	job := NewStubJob()
	dbh.InsertJob(job)
	err = ir(jid, 1)
	if err != nil {
		t.Error(err)
		return
	}
	err = ir(jid, 1)
	if err == nil {
		t.Error("Expected constraint to fail; duplicate seq id")
		return
	}
	err = ir(jid, 2)
	if err != nil {
		t.Error(err)
		return
	}
	// insert receipt for a different job, but same seqid
	job = NewStubJob()
	job.ID = 1
	dbh.InsertJob(job)
	err = ir(big.NewInt(job.ID), 1)
	if err != nil {
		t.Error(err)
		return
	}
	// check unclaimed receipts
	receipts, err := dbh.UnclaimedReceipts()
	if err != nil {
		t.Error(err)
		return
	}
	if len(receipts) != 2 {
		t.Error("Unxpected number of jobs in receipts")
		return
	}
	if len(receipts[0]) != 2 || len(receipts[1]) != 1 {
		t.Error("Unexpected number of receipts for job")
		return
	}

	// Receipt checking functions.

	// Ensure that the receipt we just inserted exists
	exists, err := dbh.ReceiptExists(big.NewInt(job.ID), 1)
	if err != nil {
		t.Error(err)
		return
	}
	if !exists {
		t.Error("Expected segment to exist in DB")
		return
	}
	// Ensure a nonexistent receipt does not exist: valid job, invalid seg
	exists, err = dbh.ReceiptExists(big.NewInt(job.ID), 10)
	if err != nil {
		t.Error(err)
		return
	}
	if exists {
		t.Error("Did not expect a segment to exist")
		return
	}
	// Ensure a nonexistent receipt does not exist: invalid job, seg exists elsewhere
	exists, err = dbh.ReceiptExists(big.NewInt(10), 1)
	if err != nil {
		t.Error(err)
		return
	}
	if exists {
		t.Error("Did not expect a segment to exist")
		return
	}
}

func TestDBClaims(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()

	ir := func(j int64, seq int) error {
		b := []byte("")
		n := time.Now()
		return dbh.InsertReceipt(big.NewInt(j), int64(seq), "", b, b, b, n, n)
	}

	job := NewStubJob()
	dbh.InsertJob(job)
	ir(job.ID, 0)
	ir(job.ID, 1)
	ir(job.ID, 4)
	ir(job.ID, 5)
	job.ID++
	dbh.InsertJob(job)
	ir(job.ID, 1)
	ir(job.ID, 2)

	cidp, err := dbh.InsertClaim(big.NewInt(0), [2]int64{0, 1}, [32]byte{})
	if err != nil || cidp == nil {
		t.Errorf("Error inserting claim %v %v", err, cidp)
		return
	}
	cidp, err = dbh.InsertClaim(big.NewInt(0), [2]int64{4, 5}, [32]byte{})
	if err != nil || cidp == nil {
		t.Errorf("Error inserting claim %v %v", err, cidp)
		return
	}
	cidp, err = dbh.InsertClaim(big.NewInt(1), [2]int64{1, 2}, [32]byte{})
	if err != nil || cidp == nil {
		t.Errorf("Error inserting claim %v %v", err, cidp)
		return
	}
	// check job 0 claim 0
	var nbreceipts int
	row := dbraw.QueryRow("SELECT count(*) FROM receipts WHERE claimID = 0 AND jobID = 0")
	err = row.Scan(&nbreceipts)
	if err != nil {
		t.Error(err)
		return
	}
	if nbreceipts != 2 {
		t.Errorf("Mismatched receipts for claim: expected 2 got %d", nbreceipts)
		return
	}
	// check job 0 claim 1
	row = dbraw.QueryRow("SELECT count(*) FROM receipts WHERE claimID = 1 AND jobID = 0")
	err = row.Scan(&nbreceipts)
	if err != nil {
		t.Error(err)
		return
	}
	if nbreceipts != 2 {
		t.Errorf("Mismatched receipts for claim: expected 2 got %d", nbreceipts)
		return
	}
	// check job 1 claim 0
	row = dbraw.QueryRow("SELECT count(*) FROM receipts WHERE claimID = 0 AND jobID = 1")
	err = row.Scan(&nbreceipts)
	if err != nil {
		t.Error(err)
		return
	}
	if nbreceipts != 2 {
		t.Errorf("Mismatched receipts for claim: expected 2 got %d", nbreceipts)
		return
	}
	// Sanity check number of claims
	var nbclaims int64
	row = dbraw.QueryRow("SELECT count(*) FROM claims")
	err = row.Scan(&nbclaims)
	if err != nil {
		t.Error(err)
		return
	}
	if nbclaims != 3 {
		t.Error("Unexpected number of claims; expected 3 total, got ", nbclaims)
		return
	}
	// check claim status
	var status string
	q := "SELECT status FROM claims WHERE jobID = 0 AND id = 1"
	s := "over the moon"
	row = dbraw.QueryRow(q)
	err = row.Scan(&status)
	if err != nil {
		t.Error(err)
		return
	}
	// sanity check value
	if status == s {
		t.Error("Expected some status value other than ", status)
		return
	}
	err = dbh.SetClaimStatus(big.NewInt(0), 1, s)
	if err != nil {
		t.Error(err)
		return
	}
	row = dbraw.QueryRow(q)
	err = row.Scan(&status)
	if err != nil || status != s {
		t.Errorf("Unexpected: error %v, got %v but wanted %v", err, status, s)
		return
	}

	// Check count claims for a given job
	nbclaims, err = dbh.CountClaims(big.NewInt(0))
	if err != nil || nbclaims != 2 {
		t.Errorf("Unexpected number of claims; expected 2 got %v; error %v", nbclaims, err)
		return
	}
	// Check count claims for a nonexistent job
	nbclaims, err = dbh.CountClaims(big.NewInt(-1))
	if err != nil || nbclaims != 0 {
		t.Errorf("Unexpected number of claims; expected 0 got %v; error %v", nbclaims, err)
	}
	// Check count claims for a job with no claims
	job.ID++
	dbh.InsertJob(job)
	nbclaims, err = dbh.CountClaims(big.NewInt(job.ID))
	if err != nil || nbclaims != 0 {
		t.Errorf("Unexpected number of claims; expected 0 got %v; error %v", nbclaims, err)
	}
}

func TestDBUnbondingLocks(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}

	delegator := ethcommon.Address{}

	// Check insertion
	err = dbh.InsertUnbondingLock(big.NewInt(0), delegator, big.NewInt(10), big.NewInt(100))
	if err != nil {
		t.Error(err)
		return
	}
	err = dbh.InsertUnbondingLock(big.NewInt(1), delegator, big.NewInt(10), big.NewInt(100))
	if err != nil {
		t.Error(err)
		return
	}
	err = dbh.InsertUnbondingLock(big.NewInt(2), delegator, big.NewInt(10), big.NewInt(100))
	if err != nil {
		t.Error(err)
		return
	}

	// Check # of unbonding locks
	var numUnbondingLocks int
	row := dbraw.QueryRow("SELECT count(*) FROM unbondingLocks")
	err = row.Scan(&numUnbondingLocks)
	if err != nil {
		t.Error(err)
		return
	}
	if numUnbondingLocks != 3 {
		t.Error("Unexpected number of unbonding locks; expected 3 total, got ", numUnbondingLocks)
		return
	}

	// Check unbonding lock IDs
	unbondingLockIDs, err := dbh.UnbondingLockIDs()
	if err != nil {
		t.Error("Error retrieving unbonding lock IDs ", err)
		return
	}
	if len(unbondingLockIDs) != 3 {
		t.Error("Unexpected number of unbonding lock IDs; expected 3, got ", len(unbondingLockIDs))
		return
	}
	if unbondingLockIDs[0].Cmp(big.NewInt(0)) != 0 {
		t.Error("Unexpected unbonding lock ID; expected 0, got ", unbondingLockIDs[0])
		return
	}

	// Check for failure with duplicate ID
	err = dbh.InsertUnbondingLock(big.NewInt(0), delegator, big.NewInt(10), big.NewInt(100))
	if err == nil {
		t.Error("Expected constraint to fail; duplicate unbonding lock ID and delegator")
		return
	}

	// Check retrieving when all unbonding locks are unused
	unbondingLocks, err := dbh.UnbondingLocks(nil)
	if err != nil {
		t.Error("Error retrieving unbonding locks ", err)
		return
	}
	if len(unbondingLocks) != 3 {
		t.Error("Unexpected number of unbonding locks; expected 3 total, got ", len(unbondingLocks))
		return
	}
	if unbondingLocks[0].ID != 0 {
		t.Error("Unexpected unbonding lock ID; expected 0, got ", unbondingLocks[0].ID)
		return
	}
	if unbondingLocks[0].Delegator != delegator {
		t.Errorf("Unexpected unbonding lock delegator; expected %v, got %v", delegator, unbondingLocks[0].Delegator)
		return
	}
	if unbondingLocks[0].Amount.Cmp(big.NewInt(10)) != 0 {
		t.Errorf("Unexpected unbonding lock amount; expected 10, got %v", unbondingLocks[0].Amount)
		return
	}
	if unbondingLocks[0].WithdrawRound != 100 {
		t.Errorf("Unexpected unbonding lock withdraw round; expected 100, got %v", unbondingLocks[0].WithdrawRound)
		return
	}

	// Check update
	err = dbh.UseUnbondingLock(big.NewInt(0), delegator, big.NewInt(15))
	if err != nil {
		t.Error(err)
		return
	}
	var usedBlock int64
	row = dbraw.QueryRow("SELECT usedBlock FROM unbondingLocks WHERE id = 0 AND delegator = ?", delegator.Hex())
	err = row.Scan(&usedBlock)
	if err != nil {
		t.Error(err)
		return
	}
	if usedBlock != 15 {
		t.Errorf("Unexpected used block; expected 15, got %v", usedBlock)
		return
	}
	err = dbh.UseUnbondingLock(big.NewInt(1), delegator, big.NewInt(16))
	if err != nil {
		t.Error(err)
		return
	}
	row = dbraw.QueryRow("SELECT usedBlock FROM unbondingLocks WHERE id = 1 AND delegator = ?", delegator.Hex())
	err = row.Scan(&usedBlock)
	if err != nil {
		t.Error(err)
		return
	}
	if usedBlock != 16 {
		t.Errorf("Unexpected used block; expected 16; got %v", usedBlock)
		return
	}

	// Check retrieving when some unbonding locks are used
	unbondingLocks, err = dbh.UnbondingLocks(nil)
	if err != nil {
		t.Error("Error retrieving unbonding locks ", err)
		return
	}
	if len(unbondingLocks) != 1 {
		t.Error("Unexpected number of unbonding locks; expected 1 total, got ", len(unbondingLocks))
		return
	}

	err = dbh.InsertUnbondingLock(big.NewInt(3), delegator, big.NewInt(10), big.NewInt(150))
	if err != nil {
		t.Error(err)
		return
	}
	err = dbh.InsertUnbondingLock(big.NewInt(4), delegator, big.NewInt(10), big.NewInt(200))
	if err != nil {
		t.Error(err)
		return
	}

	// Check retrieving withdrawable unbonding locks
	unbondingLocks, err = dbh.UnbondingLocks(big.NewInt(99))
	if err != nil {
		t.Error("Error retrieving unbonding locks ", err)
		return
	}
	if len(unbondingLocks) != 0 {
		t.Error("Unexpected number of withdrawable unbonding locks; expected 0, got ", len(unbondingLocks))
		return
	}
	unbondingLocks, err = dbh.UnbondingLocks(big.NewInt(150))
	if err != nil {
		t.Error("Error retrieving unbonding locks ", err)
		return
	}
	if len(unbondingLocks) != 2 {
		t.Error("Unexpected number of unbonding locks; expected 2, got ", len(unbondingLocks))
		return
	}
}

func TestInsertWinningTicket_GivenValidInputs_InsertsOneRowCorrectly(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}
	sessionID, ticket, sig, recipientRand := defaultWinningTicket(t)

	err = dbh.StoreWinningTicket(sessionID, ticket, sig, recipientRand)

	if err != nil {
		t.Errorf("Failed to insert winning ticket with error: %v", err)
	}

	row := dbraw.QueryRow("SELECT sender, recipient, faceValue, winProb, senderNonce, recipientRand, recipientRandHash, sig, sessionID FROM winningTickets")
	var actualSender, actualRecipient, actualRecipientRandHash, actualSessionID string
	var actualFaceValueBytes, actualWinProbBytes, actualRecipientRandBytes, actualSig []byte
	var actualSenderNonce uint32
	err = row.Scan(&actualSender, &actualRecipient, &actualFaceValueBytes, &actualWinProbBytes, &actualSenderNonce, &actualRecipientRandBytes, &actualRecipientRandHash, &actualSig, &actualSessionID)

	assert := assert.New(t)
	assert.Equal(ticket.Sender.Hex(), actualSender)
	assert.Equal(ticket.Recipient.Hex(), actualRecipient)
	assert.Equal(ticket.FaceValue, new(big.Int).SetBytes(actualFaceValueBytes))
	assert.Equal(ticket.WinProb, new(big.Int).SetBytes(actualWinProbBytes))
	assert.Equal(ticket.SenderNonce, actualSenderNonce)
	assert.Equal(recipientRand, new(big.Int).SetBytes(actualRecipientRandBytes))
	assert.Equal(ticket.RecipientRandHash, ethcommon.HexToHash(actualRecipientRandHash))
	assert.Equal(sig, actualSig)
	assert.Equal(sessionID, actualSessionID)

	ticketsCount := getRowCountOrFatal("SELECT count(*) FROM winningTickets", dbraw, t)
	assert.Equal(1, ticketsCount)
}

func TestInsertWinningTicket_GivenMaxValueInputs_InsertsOneRowCorrectly(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}

	sessionID, ticket, sig, recipientRand := defaultWinningTicket(t)
	ticket.FaceValue = maxUint256OrFatal(t)
	ticket.WinProb = maxUint256OrFatal(t)
	ticket.SenderNonce = math.MaxUint32

	err = dbh.StoreWinningTicket(sessionID, ticket, sig, recipientRand)

	if err != nil {
		t.Errorf("Failed to insert winning ticket with error: %v", err)
	}

	row := dbraw.QueryRow("SELECT sender, recipient, faceValue, winProb, senderNonce, recipientRand, recipientRandHash, sig, sessionID FROM winningTickets")
	var actualSender, actualRecipient, actualRecipientRandHash, actualSessionID string
	var actualFaceValueBytes, actualWinProbBytes, actualRecipientRandBytes, actualSig []byte
	var actualSenderNonce uint32
	err = row.Scan(&actualSender, &actualRecipient, &actualFaceValueBytes, &actualWinProbBytes, &actualSenderNonce, &actualRecipientRandBytes, &actualRecipientRandHash, &actualSig, &actualSessionID)

	assert := assert.New(t)
	assert.Equal(ticket.Sender.Hex(), actualSender)
	assert.Equal(ticket.Recipient.Hex(), actualRecipient)
	assert.Equal(ticket.FaceValue, new(big.Int).SetBytes(actualFaceValueBytes))
	assert.Equal(ticket.WinProb, new(big.Int).SetBytes(actualWinProbBytes))
	assert.Equal(ticket.SenderNonce, actualSenderNonce)
	assert.Equal(recipientRand, new(big.Int).SetBytes(actualRecipientRandBytes))
	assert.Equal(ticket.RecipientRandHash, ethcommon.HexToHash(actualRecipientRandHash))
	assert.Equal(sig, actualSig)
	assert.Equal(sessionID, actualSessionID)

	ticketsCount := getRowCountOrFatal("SELECT count(*) FROM winningTickets", dbraw, t)
	assert.Equal(1, ticketsCount)
}

func TestStoreWinningTicket_GivenNilTicket_ReturnsError(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}
	sig := randBytesOrFatal(42, t)
	recipientRand := new(big.Int).SetInt64(1234)

	err = dbh.StoreWinningTicket("sessionID", nil, sig, recipientRand)

	assert := assert.New(t)
	assert.NotNil(err)
	assert.Contains(err.Error(), "nil ticket")
}

func TestStoreWinningTicket_GivenNilSig_ReturnsError(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}
	sessionID, ticket, _, recipientRand := defaultWinningTicket(t)

	err = dbh.StoreWinningTicket(sessionID, ticket, nil, recipientRand)

	assert := assert.New(t)
	assert.NotNil(err)
	assert.Contains(err.Error(), "nil sig")
}

func TestStoreWinningTicket_GivenNilRecipientRand_ReturnsError(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}
	sessionID, ticket, sig, _ := defaultWinningTicket(t)

	err = dbh.StoreWinningTicket(sessionID, ticket, sig, nil)

	assert := assert.New(t)
	assert.NotNil(err)
	assert.Contains(err.Error(), "nil recipientRand")
}

func TestLoadtWinningTicket_GivenStoredTicket_LoadsItCorrectly(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}
	sessionID, ticket, sig, recipientRand := defaultWinningTicket(t)
	err = dbh.StoreWinningTicket(sessionID, ticket, sig, recipientRand)
	if err != nil {
		t.Fatalf("unexpected errro storing ticket: %v", err)
	}

	tickets, sigs, recipientRands, err := dbh.LoadWinningTickets(sessionID)
	if err != nil {
		t.Fatalf("unexpected error loading tickets: %v", err)
	}
	if len(tickets) != 1 || len(sigs) != 1 || len(recipientRands) != 1 {
		t.Errorf("expected one item per slice but got unexpected number of results. tickets: %d, sigs: %d, recipientRands: %d", len(tickets), len(sigs), len(recipientRands))
	}
	actualTicket := tickets[0]
	actualSig := sigs[0]
	actualRecipientRand := recipientRands[0]

	assert := assert.New(t)
	assert.Equal(ticket, actualTicket)
	assert.Equal(sig, actualSig)
	assert.Equal(recipientRand, actualRecipientRand)
}

func TestLoadtWinningTicket_GivenStoredTicketsFromDifferentSessions_OnlyLoadsFromSpecificSessionID(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}

	// Two tickets in the first session
	firstSessionID := "first session"
	_, ticket0, sig0, recipientRand0 := defaultWinningTicket(t)
	err = dbh.StoreWinningTicket(firstSessionID, ticket0, sig0, recipientRand0)
	if err != nil {
		t.Fatalf("unexpected errro storing ticket: %v", err)
	}

	_, ticket1, sig1, recipientRand1 := defaultWinningTicket(t)
	err = dbh.StoreWinningTicket(firstSessionID, ticket1, sig1, recipientRand1)
	if err != nil {
		t.Fatalf("unexpected errro storing ticket: %v", err)
	}

	// One ticket in the second session
	secondSessionID := "second session"
	_, ticket2, sig2, recipientRand2 := defaultWinningTicket(t)
	err = dbh.StoreWinningTicket(secondSessionID, ticket2, sig2, recipientRand2)
	if err != nil {
		t.Fatalf("unexpected errro storing ticket: %v", err)
	}

	tickets, sigs, recipientRands, err := dbh.LoadWinningTickets(firstSessionID)

	assert := assert.New(t)
	assert.Nil(err)
	assert.Len(tickets, 2)
	assert.Len(sigs, 2)
	assert.Len(recipientRands, 2)
	assert.Equal(ticket0, tickets[0])
	assert.Equal(sig0, sigs[0])
	assert.Equal(recipientRand0, recipientRands[0])
	assert.Equal(ticket1, tickets[1])
	assert.Equal(sig1, sigs[1])
	assert.Equal(recipientRand1, recipientRands[1])
}

func TestLoadtWinningTicket_GivenNonexistentSessionID_ReturnsEmptySlicesNoError(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}

	tickets, sigs, recipientRands, err := dbh.LoadWinningTickets("some sessionID")

	assert := assert.New(t)
	assert.Nil(err)
	assert.Len(tickets, 0)
	assert.Len(sigs, 0)
	assert.Len(recipientRands, 0)
}

func TestLoadtWinningTicket_GivenEmptySessionID_ReturnsEmptySlicesNoError(t *testing.T) {
	dbh, dbraw, err := TempDB(t)
	defer dbh.Close()
	defer dbraw.Close()
	if err != nil {
		t.Error(err)
		return
	}

	tickets, sigs, recipientRands, err := dbh.LoadWinningTickets("")

	assert := assert.New(t)
	assert.Nil(err)
	assert.Len(tickets, 0)
	assert.Len(sigs, 0)
	assert.Len(recipientRands, 0)
}

func defaultWinningTicket(t *testing.T) (sessionID string, ticket *pm.Ticket, sig []byte, recipientRand *big.Int) {
	sessionID = "foo bar"
	ticket = &pm.Ticket{
		Sender:            randAddressOrFatal(t),
		Recipient:         randAddressOrFatal(t),
		FaceValue:         big.NewInt(1234),
		WinProb:           big.NewInt(2345),
		SenderNonce:       uint32(123),
		RecipientRandHash: pm.RandHashOrFatal(t),
	}
	sig = randBytesOrFatal(42, t)
	recipientRand = big.NewInt(4567)
	return
}

func getRowCountOrFatal(query string, dbraw *sql.DB, t *testing.T) int {
	var count int
	row := dbraw.QueryRow(query)
	err := row.Scan(&count)

	if err != nil {
		t.Fatalf("error getting db table count, cannot proceed: %v", err)
		return -1
	}

	return count
}

func randAddressOrFatal(t *testing.T) ethcommon.Address {
	key, err := randBytes(20)

	if err != nil {
		t.Fatalf("failed generating random address: %v", err)
		return ethcommon.Address{}
	}

	return ethcommon.BytesToAddress(key[:])
}

func randBytesOrFatal(size int, t *testing.T) []byte {
	res, err := randBytes(size)

	if err != nil {
		t.Fatalf("failed generating random bytes: %v", err)
		return nil
	}

	return res
}

func randBytes(size int) ([]byte, error) {
	key := make([]byte, size)
	_, err := rand.Read(key)

	return key, err
}

func maxUint256OrFatal(t *testing.T) *big.Int {
	n, ok := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
	if !ok {
		t.Fatalf("unexpected error creating max value of uint256")
	}
	return n
}
