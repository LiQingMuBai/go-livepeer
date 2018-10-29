package eth

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/golang/glog"
	"github.com/livepeer/go-livepeer/common"
	ethTypes "github.com/livepeer/go-livepeer/eth/types"
	"github.com/livepeer/go-livepeer/ipfs"
	ffmpeg "github.com/livepeer/lpms/ffmpeg"
)

func newJob() *ethTypes.Job {
	ps := []ffmpeg.VideoProfile{ffmpeg.P240p30fps16x9, ffmpeg.P360p30fps4x3, ffmpeg.P720p30fps4x3}
	return &ethTypes.Job{
		JobId:              big.NewInt(5),
		StreamId:           "strmID",
		Profiles:           ps,
		MaxPricePerSegment: big.NewInt(1),
		BroadcasterAddress: ethcommon.Address{},
		TotalClaims:        big.NewInt(0),
		CreationBlock:      big.NewInt(100),
		EndBlock:           big.NewInt(200),
	}
}

func TestShouldVerify(t *testing.T) {
	client := &StubClient{}
	cm := NewBasicClaimManager(newJob(), client, &ipfs.StubIpfsApi{}, nil)

	blkHash := ethcommon.Hash([32]byte{0, 2, 4, 42, 2, 3, 4, 4, 4, 2, 21, 1, 1, 24, 134, 0, 02, 43})
	//Just make sure the results are different
	same := true
	var result bool
	for i := int64(0); i < 10; i++ {
		if tResult := cm.shouldVerifySegment(i, 0, 10, 100, blkHash, 5); result != tResult {
			same = false
			break
		} else {
			result = tResult
		}
	}
	if same {
		t.Errorf("Should give different results")
	}
}

func TestProfileOrder(t *testing.T) {
	cm := NewBasicClaimManager(newJob(), &StubClient{}, &ipfs.StubIpfsApi{}, nil)

	if cm.profiles[0] != ffmpeg.P720p30fps4x3 || cm.profiles[1] != ffmpeg.P360p30fps4x3 || cm.profiles[2] != ffmpeg.P240p30fps16x9 {
		t.Errorf("wrong ordering: %v", cm.profiles)
	}
}

func hashTData(order []ffmpeg.VideoProfile, td map[ffmpeg.VideoProfile][]byte) []byte {
	hashes := make([][]byte, len(td))
	for i, p := range order {
		hashes[i] = crypto.Keccak256(td[p])
	}
	return crypto.Keccak256(hashes...)
}

func TestAddReceipt(t *testing.T) {
	db, _ := common.InitDB(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())) // NOTE foreign keys are disabled; enable if a test begins depending on them
	ps := []ffmpeg.VideoProfile{ffmpeg.P240p30fps16x9, ffmpeg.P360p30fps4x3, ffmpeg.P720p30fps4x3}
	cm := NewBasicClaimManager(newJob(), &StubClient{}, &ipfs.StubIpfsApi{}, db)
	tStart := time.Now().UTC()
	tEnd := tStart
	defer db.Close()

	bcastFile, err := ioutil.TempFile("", "data")
	if err != nil {
		t.Error("Unable to open tempfile ", err)
	}
	bf := bcastFile.Name()
	defer os.Remove(bf)
	bd, err := ioutil.ReadAll(bcastFile)
	if err != nil {
		t.Error("Unable to read tempfile ", err)
	}
	bcastFile.Close()

	// Sanity check the file that is read hashes to what's expected
	if bytes.Equal(crypto.Keccak256(bd), crypto.Keccak256([]byte("data"))) {
		t.Error("File data hash did not match raw hash")
	}

	//Should get error due to a length mismatch
	td := map[ffmpeg.VideoProfile][]byte{
		ffmpeg.P360p30fps4x3:  []byte("tdatahash"),
		ffmpeg.P240p30fps16x9: []byte("tdatahash"),
	}
	if _, err := cm.AddReceipt(0, "", []byte("data"), []byte("sig"), td, tStart, tEnd); err == nil {
		t.Error("Expecting an error for mismatched profile legnths.")
	}
	//Should get error for adding to a non-existing profile
	td[ffmpeg.P144p30fps16x9] = []byte("tdatahash")
	if _, err := cm.AddReceipt(0, "", []byte("data"), []byte("sig"), td, tStart, tEnd); err == nil {
		t.Error("Expecting an error for adding to a non-existing profile.")
	}
	// Should pass
	td = map[ffmpeg.VideoProfile][]byte{
		ffmpeg.P360p30fps4x3:  []byte("tdatahash"),
		ffmpeg.P240p30fps16x9: []byte("tdatahash"),
		ffmpeg.P720p30fps4x3:  []byte("tdatahash"),
	}
	if _, err := cm.AddReceipt(0, bf, []byte("data"), []byte("sig"), td, tStart, tEnd); err != nil {
		t.Error("Unexpected error ", err)
	}
	// Should get an error due to an already existing receipt
	if _, err := cm.AddReceipt(0, "", []byte("data"), []byte("sig"), td, tStart, tEnd); err == nil {
		t.Error("Did not get an error where one was expected")
	}

	if string(cm.segClaimMap[0].bFile) != bf {
		t.Errorf("Expecting %v, got %v", cm.segClaimMap[0].bFile, bf)
	}

	if string(cm.segClaimMap[0].dataHash) != string(crypto.Keccak256([]byte("data"))) { //appended by ipfs.StubIpfsApi
		t.Errorf("Expecting %v, got %v", string(crypto.Keccak256([]byte("data"))), string(cm.segClaimMap[0].dataHash))
	}

	hash := hashTData(ps, td)
	if string(cm.segClaimMap[0].claimConcatTDatahash) != string(hash) {
		t.Errorf("Expecting %v, got %v", ethcommon.ToHex(hash), ethcommon.ToHex(cm.segClaimMap[0].claimConcatTDatahash))
	}

	if string(cm.segClaimMap[0].bSig) != "sig" {
		t.Errorf("Expecting %v, got %v", "sig", string(cm.segClaimMap[0].bSig))
	}

	// Simulate recovery: insert a receipt in the DB then insert another with
	// the same seqNo the normal way. Should fail due to double claims

	// Sanity check that the segment doesn't exist already
	if _, ok := cm.unclaimedSegs[1]; ok {
		t.Error("Expected segment 1 to be unclaimed")
	}
	// fake it with the db insertion
	err = db.InsertReceipt(cm.jobID, 1, "", []byte{}, []byte{}, []byte{}, tStart, tEnd)
	if err != nil {
		t.Error(err)
	}
	// normal insertion
	_, err = cm.AddReceipt(1, "", []byte{}, []byte{}, td, tStart, tEnd)
	if err == nil {
		t.Error("Expecting error; inserting duplicate claim!")
	}
	// ensure we don't have data inadvertedly sitting around our data structures
	if _, ok := cm.unclaimedSegs[1]; ok {
		t.Error("Expected segment 1 to be unclaimed")
	}
	if _, ok := cm.segClaimMap[1]; ok {
		t.Error("Expected segment 1 to be unclaimed")
	}
}

//We add 6 ranges (0, 3-13, 15-18, 20-25, 27, 29)
func setupRanges(t *testing.T) *BasicClaimManager {
	ethClient := &StubClient{ClaimStart: make([]*big.Int, 0), ClaimEnd: make([]*big.Int, 0), ClaimJid: make([]*big.Int, 0), ClaimRoot: make(map[[32]byte]bool)}
	ps := []ffmpeg.VideoProfile{ffmpeg.P240p30fps16x9, ffmpeg.P360p30fps4x3, ffmpeg.P720p30fps4x3}
	cm := NewBasicClaimManager(newJob(), ethClient, &ipfs.StubIpfsApi{}, nil)
	tStart := time.Now().UTC()
	tEnd := tStart

	for _, segRange := range [][2]int64{[2]int64{0, 0}, [2]int64{3, 13}, [2]int64{15, 18}, [2]int64{21, 25}, [2]int64{27, 27}, [2]int64{29, 29}} {
		for i := segRange[0]; i <= segRange[1]; i++ {
			td := map[ffmpeg.VideoProfile][]byte{}
			for _, p := range ps {
				td[p] = []byte(fmt.Sprintf("hash%v%v", p.Name, i))
			}
			data := []byte(fmt.Sprintf("data%v", i))
			sig := []byte(fmt.Sprintf("sig%v", i))
			if _, err := cm.AddReceipt(int64(i), "", data, sig, td, tStart, tEnd); err != nil {
				t.Errorf("Error: %v", err)
			}
			if i == 16 {
				glog.Infof("data: %v", data)
			}
		}
	}

	//Add invalid 19 out of order (invalid because it's only a single video profile)
	i := 19
	p := ffmpeg.P360p30fps4x3
	data := []byte(fmt.Sprintf("data%v%v", p.Name, i))
	sig := []byte(fmt.Sprintf("sig%v%v", p.Name, i))
	td := map[ffmpeg.VideoProfile][]byte{
		p: []byte(fmt.Sprintf("hash%v%v", p, i)),
	}
	if _, err := cm.AddReceipt(int64(i), "", data, sig, td, tStart, tEnd); err == nil {
		t.Errorf("Did not get an error when expecting one")
	}

	return cm
}

func TestRanges(t *testing.T) {
	//We added 6 ranges (0, 3-13, 15-18, 20-25, 27, 29)
	cm := setupRanges(t)
	ranges := cm.makeRanges()
	glog.Infof("ranges: %v", ranges)
	if len(ranges) != 6 {
		t.Errorf("Expecting 6 ranges, got %v", ranges)
	}

	// No unclaimed segs; this crashed at one point.
	cm.unclaimedSegs = map[int64]bool{}
	ranges = cm.makeRanges()
	if len(ranges) != 0 {
		t.Error("Expected 0 ranges, got ", ranges)
	}
}

func TestClaimVerifyAndDistributeFees(t *testing.T) {
	ethClient := &StubClient{ClaimStart: make([]*big.Int, 0), ClaimEnd: make([]*big.Int, 0), ClaimJid: make([]*big.Int, 0), ClaimRoot: make(map[[32]byte]bool), Claims: make(map[int]*ethTypes.Claim)}
	ps := []ffmpeg.VideoProfile{ffmpeg.P240p30fps16x9, ffmpeg.P360p30fps4x3, ffmpeg.P720p30fps4x3}
	cm := NewBasicClaimManager(newJob(), ethClient, &ipfs.StubIpfsApi{}, nil)
	tStart := time.Now().UTC()
	tEnd := tStart

	//Add some receipts(0-9)
	receiptHashes1 := make([]ethcommon.Hash, 10)
	for i := 0; i < 10; i++ {
		td := make(map[ffmpeg.VideoProfile][]byte, len(ps))
		data := []byte(fmt.Sprintf("data%v", i))
		sig := []byte(fmt.Sprintf("sig%v", i))
		for _, p := range ps {
			td[p] = []byte(fmt.Sprintf("tHash%v%v", ffmpeg.P240p30fps16x9.Name, i)) // ???
		}
		if _, err := cm.AddReceipt(int64(i), "", data, sig, td, tStart, tEnd); err != nil {
			t.Errorf("Error: %v", err)
		}

		receipt := &ethTypes.TranscodeReceipt{
			StreamID:                 "strmID",
			SegmentSequenceNumber:    big.NewInt(int64(i)),
			DataHash:                 crypto.Keccak256(data),
			ConcatTranscodedDataHash: hashTData(ps, td),
			BroadcasterSig:           []byte(sig),
		}
		receiptHashes1[i] = receipt.Hash()
	}

	//Add some receipts(15-24)
	receiptHashes2 := make([]ethcommon.Hash, 10)
	for i := 15; i < 25; i++ {
		td := map[ffmpeg.VideoProfile][]byte{}
		data := []byte(fmt.Sprintf("data%v", i))
		sig := []byte(fmt.Sprintf("sig%v", i))
		for _, p := range ps {
			td[p] = []byte(fmt.Sprintf("tHash%v%v", ffmpeg.P240p30fps16x9.Name, i)) // ???
		}
		if _, err := cm.AddReceipt(int64(i), "", data, sig, td, tStart, tEnd); err != nil {
			t.Errorf("Error: %v", err)
		}
		receipt := &ethTypes.TranscodeReceipt{
			StreamID:                 "strmID",
			SegmentSequenceNumber:    big.NewInt(int64(i)),
			DataHash:                 crypto.Keccak256(data),
			ConcatTranscodedDataHash: hashTData(ps, td),
			BroadcasterSig:           []byte(sig),
		}
		receiptHashes2[i-15] = receipt.Hash()
	}

	err := cm.ClaimVerifyAndDistributeFees()
	if err != nil {
		t.Fatal(err)
	}

	//Make sure the roots are used for calling Claim
	root1, _, err := ethTypes.NewMerkleTree(receiptHashes1)
	if err != nil {
		t.Errorf("Error: %v", err)
	}
	if _, ok := ethClient.ClaimRoot[[32]byte(root1.Hash)]; !ok {
		t.Errorf("Expecting claim to have root %v, but got %v", [32]byte(root1.Hash), ethClient.ClaimRoot)
	}

	root2, _, err := ethTypes.NewMerkleTree(receiptHashes2)
	if err != nil {
		t.Errorf("Error: %v", err)
	}
	if _, ok := ethClient.ClaimRoot[[32]byte(root2.Hash)]; !ok {
		t.Errorf("Expecting claim to have root %v, but got %v", [32]byte(root2.Hash), ethClient.ClaimRoot)
	}
}

func TestRecoverClaims(t *testing.T) {
	// this whole thing is timing sensitive; fix?

	sc := &StubClient{ClaimRoot: make(map[[32]byte]bool), Claims: make(map[int]*ethTypes.Claim)}
	db, dbraw, err := common.TempDB(t)
	if err != nil {
		t.Error(t)
	}
	defer db.Close()
	defer dbraw.Close()

	// empty db
	err = RecoverClaims(sc, &ipfs.StubIpfsApi{}, db)
	if err != nil {
		t.Error(t)
	}

	// claims are async so sleep for a bit
	time.Sleep(1 * time.Second)

	var claims int
	err = dbraw.QueryRow("SELECT count(*) FROM claims").Scan(&claims)
	if err != nil || claims != 0 {
		t.Errorf("Unexpected claims got %v error %v", claims, err)
	}

	j := newJob()
	jid := j.JobId
	db.InsertJob(EthJobToDBJob(j))

	db.InsertReceipt(jid, 1, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())
	db.InsertReceipt(jid, 2, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())
	db.InsertReceipt(jid, 3, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())
	db.InsertReceipt(jid, 8, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())
	db.InsertReceipt(jid, 9, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())

	j.JobId = big.NewInt(0).Add(jid, jid)
	db.InsertJob(EthJobToDBJob(j))

	db.InsertReceipt(j.JobId, 1, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())
	db.InsertReceipt(j.JobId, 2, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())

	// sanity check receipts and claims in db
	receipts, err := db.UnclaimedReceipts()
	if err != nil || (len(receipts) != 2 && len(receipts[0]) != 5 && len(receipts[1]) != 2) {
		t.Error("Receipts ", err)
	}
	jc, err := db.CountClaims(jid)
	if err != nil || jc != 0 {
		t.Error("Claims ", err)
	}

	// TODO Claims recovery is async for each job and thus timing sensitive
	// 		Fails often on CI. Fix!
	return

	err = RecoverClaims(sc, &ipfs.StubIpfsApi{}, db)
	if err != nil {
		t.Error(t)
	}

	// claims are async so sleep for a bit
	time.Sleep(1 * time.Second)

	dbraw.QueryRow("SELECT count(*) FROM claims").Scan(&claims)
	jc1, _ := db.CountClaims(jid)
	jc2, _ := db.CountClaims(j.JobId)
	if claims != 3 || sc.ClaimCounter != 3 || jc1 != 2 || jc2 != 1 {
		t.Error("Unexpected claims ", claims, sc.ClaimCounter, jc1, jc2)
	}

	// all receipts should have been claimed
	receipts, err = db.UnclaimedReceipts()
	if err != nil || len(receipts) != 0 {
		t.Error("Receipts ", err, len(receipts))
	}

	// ensure imdepotency
	err = RecoverClaims(sc, &ipfs.StubIpfsApi{}, db)
	if err != nil {
		t.Error(t)
	}

	// claims are async so sleep for a bit
	time.Sleep(1 * time.Second)

	err = dbraw.QueryRow("SELECT count(*) FROM claims").Scan(&claims)
	if err != nil || claims != 3 || sc.ClaimCounter != 3 {
		t.Errorf("Unexpected claims got %v error %v", claims, err)
	}

	// now move forward the block count and check if claims can be submitted
	db.SetLastSeenBlock(big.NewInt(1000))

	// existing job that has submitted a claim; can submit another
	db.InsertReceipt(jid, 4, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())
	dbj, _ := db.GetJob(jid.Int64())
	j = common.DBJobToEthJob(dbj)
	lsb, _ := db.LastSeenBlock()
	if lsb.Int64()-j.CreationBlock.Int64() <= 256 { // sanity check
		t.Error("Not > 256 blocks after creation")
	}

	// new job, blocks > 256 and hasn't submitted a claim; can't claim anymore
	j.JobId = big.NewInt(99)
	db.InsertJob(EthJobToDBJob(j))
	db.InsertReceipt(j.JobId, 1, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())
	db.InsertReceipt(j.JobId, 2, "bcastfile", []byte("sig"), []byte("tcodehash"), []byte("bcasthash"), time.Now(), time.Now())

	receipts, _ = db.UnclaimedReceipts()
	if len(receipts) != 2 && len(receipts[0]) != 1 && len(receipts[1]) != 2 {
		t.Error("Receipts after 256 blocks")
	}

	err = RecoverClaims(sc, &ipfs.StubIpfsApi{}, db)
	if err != nil {
		t.Error(t)
	}

	// claims are async so sleep for a bit
	time.Sleep(1 * time.Second)

	dbraw.QueryRow("SELECT count(*) FROM claims").Scan(&claims)
	jc1, _ = db.CountClaims(jid)
	jc2, _ = db.CountClaims(j.JobId)
	if claims != 4 || sc.ClaimCounter != 4 || jc1 != 3 || jc2 != 0 {
		t.Error("Unexpected claims ", claims, sc.ClaimCounter, jc1, jc2)
	}
}

// func TestVerify(t *testing.T) {
// 	ethClient := &StubClient{VeriRate: 10}
// 	ps := []ffmpeg.VideoProfile{ffmpeg.P240p30fps16x9, ffmpeg.P360p30fps4x3, ffmpeg.P720p30fps4x3}
// 	cm := NewBasicClaimManager("strmID", big.NewInt(5), ethcommon.Address{}, big.NewInt(1), ps, ethClient, &ipfs.StubIpfsApi{}, nil)
// 	start := int64(0)
// 	end := int64(100)
// 	blkNum := int64(100)
// 	seqNum := int64(10)
// 	//Find a blkHash that will trigger verification
// 	var blkHash ethcommon.Hash
// 	for i := 0; i < 100; i++ {
// 		blkHash = ethcommon.HexToHash(fmt.Sprintf("0xDEADBEEF%v", i))
// 		if cm.shouldVerifySegment(seqNum, start, end, blkNum, blkHash, 10) {
// 			break
// 		}
// 	}

// 	//Add a seg that shouldn't trigger
// 	cm.AddReceipt(seqNum, []byte("data240"), []byte("tDataHash240"), []byte("bSig"), ffmpeg.P240p30fps16x9)
// 	cm.AddReceipt(seqNum, []byte("data360"), []byte("tDataHash360"), []byte("bSig"), ffmpeg.P360p30fps4x3)
// 	cm.AddReceipt(seqNum, []byte("data720"), []byte("tDataHash720"), []byte("bSig"), ffmpeg.P720p30fps4x3)
// 	seg, _ := cm.segClaimMap[seqNum]
// 	seg.claimStart = start
// 	seg.claimEnd = end
// 	seg.claimBlkNum = big.NewInt(blkNum)
// 	ethClient.BlockHashToReturn = ethcommon.Hash{}

// 	if err := cm.Verify(); err != nil {
// 		t.Errorf("Error: %v", err)
// 	}
// 	if ethClient.VerifyCounter != 0 {
// 		t.Errorf("Expect verify to NOT be triggered")
// 	}

// 	// //Add a seg that SHOULD trigger
// 	cm.AddReceipt(seqNum, []byte("data240"), []byte("tDataHash240"), []byte("bSig"), ffmpeg.P240p30fps16x9)
// 	cm.AddReceipt(seqNum, []byte("data360"), []byte("tDataHash360"), []byte("bSig"), ffmpeg.P360p30fps4x3)
// 	cm.AddReceipt(seqNum, []byte("data720"), []byte("tDataHash720"), []byte("bSig"), ffmpeg.P720p30fps4x3)
// 	seg, _ = cm.segClaimMap[seqNum]
// 	seg.claimStart = start
// 	seg.claimEnd = end
// 	seg.claimBlkNum = big.NewInt(blkNum)
// 	seg.claimProof = []byte("proof")
// 	ethClient.BlockHashToReturn = blkHash

// 	if err := cm.Verify(); err != nil {
// 		t.Errorf("Error: %v", err)
// 	}
// 	lpCommon.WaitUntil(100*time.Millisecond, func() bool {
// 		return ethClient.VerifyCounter != 0
// 	})
// 	if ethClient.VerifyCounter != 1 {
// 		t.Errorf("Expect verify to be triggered")
// 	}
// 	if string(ethClient.Proof) != string(seg.claimProof) {
// 		t.Errorf("Expect proof to be %v, got %v", seg.claimProof, ethClient.Proof)
// 	}
// }
