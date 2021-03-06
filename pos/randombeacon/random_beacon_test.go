package randombeacon

import (
	"github.com/wanchain/go-wanchain/accounts/keystore"
	accBn256 "github.com/wanchain/go-wanchain/accounts/keystore/bn256"
	"github.com/wanchain/go-wanchain/core/vm"
	"github.com/wanchain/go-wanchain/crypto"
	"github.com/wanchain/go-wanchain/crypto/bn256/cloudflare"
	"github.com/wanchain/go-wanchain/pos/epochLeader"
	"github.com/wanchain/go-wanchain/pos/posconfig"
	"github.com/wanchain/go-wanchain/pos/rbselection"
	"math/big"
	"testing"
)

var (
	selfPrivate      *accBn256.PrivateKeyBn256
	commityPrivate   *accBn256.PrivateKeyBn256
	hbase            = new(bn256.G2).ScalarBaseMult(big.NewInt(int64(1)))
	ens              = make([][]*bn256.G1, 0)
	commit           [][]bn256.G2
)

func TestInit(t *testing.T) {
	var epocher epochLeader.Epocher
	var key keystore.Key
	var rb RandomBeacon

	var err error
	key.PrivateKey3, err = accBn256.GenerateBn256()
	if err != nil {
		t.Error("generate bn256 fail, ", err)
	}

	rb.Init(&epocher)

	if rb.epochStage != vm.RbDkg1Stage {
		t.Error("invalid epoch stage")
	}

	if rb.epochId != maxUint64 {
		t.Error("invalid init epoch id")
	}

	if rb.statedb != nil {
		t.Error("invalid init statedb")
	}

	if rb.epocher != &epocher {
		t.Error("invalid rb epocher")
	}

	if rb.rpcClient != nil {
		t.Error("invalid rb rpc client")
	}
}

func tmpGetRBProposerGroup(epochId uint64) []bn256.G1 {
	ret := make([]bn256.G1, posconfig.RandomProperCount)
	for i := 0; i < posconfig.RandomProperCount; i++ {
		ret[i] = *commityPrivate.PublicKeyBn256.G1
	}

	return ret
}

func TestGetMyRBProposerId(t *testing.T) {
	var epocher epochLeader.Epocher
	var key keystore.Key
	var rb RandomBeacon

	var err error
	key.PrivateKey3, err = accBn256.GenerateBn256()
	if err != nil {
		t.Error("generate bn256 fail, ", err)
	}

	selfPrivate = key.PrivateKey3
	posconfig.Cfg().MinerKey = &key

	commityPrivate, err = accBn256.GenerateBn256()
	if err != nil {
		t.Error("generate bn256 fail, ", err)
	}

	rb.Init(&epocher)
	rb.getRBProposerGroupF = tmpGetRBProposerGroup

	rb.myPropserIds = rb.getMyRBProposerId(0)
	println("ids len:", len(rb.myPropserIds))
	if len(rb.myPropserIds) != 0 {
		t.Error("invalid my proposer id")
	}

	commityPrivate = key.PrivateKey3
	rb.myPropserIds = rb.getMyRBProposerId(0)
	println("ids len:", len(rb.myPropserIds))
	if len(rb.myPropserIds) != posconfig.RandomProperCount {
		t.Error("invalid my proposer id group len. expect len:", posconfig.RandomProperCount, ", acture:", len(rb.myPropserIds))
	}

	for i := 0; i < len(rb.myPropserIds); i++ {
		println("ids[", i, "]:", rb.myPropserIds[i])
		if rb.myPropserIds[i] != uint32(i) {
			t.Error("invalid my proposer id. expect:", i, ", acture:", rb.myPropserIds[i])
			break
		}
	}
}

func TestDoGenerateDKG1(t *testing.T) {
	var epocher epochLeader.Epocher
	var key keystore.Key
	var rb RandomBeacon

	var err error
	key.PrivateKey3, err = accBn256.GenerateBn256()
	if err != nil {
		t.Error("generate bn256 fail, ", err)
	}

	selfPrivate = key.PrivateKey3
	commityPrivate = selfPrivate
	posconfig.Cfg().MinerKey = &key

	rb.Init(&epocher)
	rb.getRBProposerGroupF = tmpGetRBProposerGroup
	rb.getCji = tmpGetCji

	rb.epochId = uint64(0)

	// pks
	rb.proposerPks = rb.getRBProposerGroupF(rb.epochId)
	nr := len(rb.proposerPks)
	rb.myPropserIds = rb.getMyRBProposerId(rb.epochId)

	// x
	x := make([]big.Int, nr)
	for i := 0; i < nr; i++ {
		x[i].SetBytes(vm.GetPolynomialX(&rb.proposerPks[i], uint32(i)))
		x[i].Mod(&x[i], bn256.Order)
	}

	commit = make([][]bn256.G2, nr)
	// generate every dkg1 and verify it
	for proposerId := 0; proposerId < nr; proposerId++ {
		payload, err := rb.generateDKG1(uint32(proposerId))
		if err != nil {
			t.Fatal("rb generate dkg info fail. err:", err)
		}

		if payload == nil {
			t.Fatal("rb generate dkg info is nil")
		}

		// verify
		if payload.EpochId != rb.epochId || payload.ProposerId != uint32(proposerId) {
			t.Error("invalid epochId proposerId")
		}

		// Reed-Solomon code verification
		dkg1, err := vm.Dkg1FlatToDkg1(payload)
		if err != nil {
			t.Error("trans dkg1flat to dkg1 fail. err:", err)
		}

		commit[proposerId] = make([]bn256.G2, nr)
		for j := 0; j < nr; j++ {
			commit[proposerId][j] = *dkg1.Commit[j]
		}

		if !rbselection.RScodeVerify(commit[proposerId], x, int(posconfig.Cfg().PolymDegree)) {
			t.Error("reed solomon verification fail")
		}

	}
}

func TestGenerateDKG2(t *testing.T) {
	var epocher epochLeader.Epocher
	var key keystore.Key
	var rb RandomBeacon

	dkg1s := make([]*vm.RbDKG1TxPayload, 0)

	var err error
	key.PrivateKey3, err = accBn256.GenerateBn256()
	if err != nil {
		t.Error("generate bn256 fail, ", err)
	}

	selfPrivate = key.PrivateKey3
	commityPrivate = selfPrivate
	posconfig.Cfg().MinerKey = &key

	rb.Init(&epocher)
	rb.getRBProposerGroupF = tmpGetRBProposerGroup
	rb.getCji = tmpGetCji

	epochId := uint64(0)
	rb.epochId = epochId

	// pks
	rb.proposerPks = rb.getRBProposerGroupF(epochId)
	nr := len(rb.proposerPks)

	// x
	x := make([]big.Int, nr)
	for i := 0; i < nr; i++ {
		x[i].SetBytes(vm.GetPolynomialX(&rb.proposerPks[i], uint32(i)))
		x[i].Mod(&x[i], bn256.Order)
	}

	commit = make([][]bn256.G2, nr)

	// generate every dkg1 and verify it
	for proposerId := 0; proposerId < nr; proposerId++ {
		dkg1Flat, err := rb.generateDKG1(uint32(proposerId))
		if err != nil {
			t.Fatal("rb generate dkg1 info fail. err:", err)
		}

		if dkg1Flat == nil {
			t.Fatal("rb generate dkg1 info is nil")
		}

		dkg1, err := vm.Dkg1FlatToDkg1(dkg1Flat)
		if err != nil {
			t.Fatal("trans dkg1flat to dkg1 fail. err:", err)
		}

		commit[proposerId] = make([]bn256.G2, nr)
		for j := 0; j < nr; j++ {
			commit[proposerId][j] = *dkg1.Commit[j]
		}

		dkg1s = append(dkg1s, dkg1)
	}

	for proposerId := 0; proposerId < nr; proposerId++ {
		dkg2Flat, err := rb.generateDKG2(uint32(proposerId))
		if err != nil {
			t.Fatal("rb generate dkg2 fail. err:", err)
		}

		if dkg2Flat == nil {
			t.Fatal("rb generate dkg2 info is nil")
		}

		// verify
		// EnShare, Commit, Proof has the same size
		// check same size
		if nr != len(dkg2Flat.EnShare) {
			t.Fatal("dkg2 params have different length")
		}

		dkg2, err := vm.Dkg2FlatToDkg2(dkg2Flat)
		if err != nil {
			t.Fatal("transf dkg2flat to dkg2 fail, err:", err)
		}

		// proof verification
		for j := 0; j < nr; j++ {
			// get send public Key
			if !rbselection.VerifyDLEQ(dkg2.Proof[j], rb.proposerPks[j], *hbase, *dkg2.EnShare[j], *(dkg1s[proposerId].Commit[j])) {
				t.Fatal("dkg2 DLEQ verify fail")
			}
		}
	}
}

func TestGenerateSIG(t *testing.T) {
	var epocher epochLeader.Epocher
	var key keystore.Key
	var rb RandomBeacon

	dkg1s := make([]*vm.RbDKG1TxPayload, 0)
	dkg2s := make([]*vm.RbDKG2TxPayload, 0)

	var err error
	key.PrivateKey3, err = accBn256.GenerateBn256()
	if err != nil {
		t.Error("generate bn256 fail, ", err)
	}

	selfPrivate = key.PrivateKey3
	commityPrivate = selfPrivate
	posconfig.Cfg().MinerKey = &key

	rb.Init(&epocher)
	rb.getRBProposerGroupF = tmpGetRBProposerGroup
	rb.getEns = tmpGetEnsFunc
	rb.getRBM = tmpGetRBM
	rb.getCji = tmpGetCji

	rb.epochId = uint64(0)

	// pks
	rb.proposerPks = rb.getRBProposerGroupF(rb.epochId)
	nr := len(rb.proposerPks)

	// x
	x := make([]big.Int, nr)
	for i := 0; i < nr; i++ {
		x[i].SetBytes(vm.GetPolynomialX(&rb.proposerPks[i], uint32(i)))
		x[i].Mod(&x[i], bn256.Order)
	}

	commit = make([][]bn256.G2, nr)

	// generate every dkg1 and verify it
	for proposerId := 0; proposerId < nr; proposerId++ {
		dkg1Flat, err := rb.generateDKG1(uint32(proposerId))
		if err != nil {
			t.Fatal("rb generate dkg1 info fail. err:", err)
		}

		if dkg1Flat == nil {
			t.Fatal("rb generate dkg1 info is nil")
		}

		dkg1, err := vm.Dkg1FlatToDkg1(dkg1Flat)
		if err != nil {
			t.Fatal("trans dkg1flat to dkg1 fail. err:", err)
		}

		commit[proposerId] = make([]bn256.G2, nr)
		for j := 0; j < nr; j++ {
			commit[proposerId][j] = *dkg1.Commit[j]
		}

		dkg1s = append(dkg1s, dkg1)
	}

	for proposerId := 0; proposerId < nr; proposerId++ {
		dkg2Flat, err := rb.generateDKG2(uint32(proposerId))
		if err != nil {
			t.Fatal("rb generate dkg2 fail. err:", err)
		}

		if dkg2Flat == nil {
			t.Fatal("rb generate dkg2 info is nil")
		}

		dkg2, err := vm.Dkg2FlatToDkg2(dkg2Flat)
		if err != nil {
			t.Fatal("transf dkg2flat to dkg2 fail, err:", err)
		}

		dkg2s = append(dkg2s, dkg2)
		ens = append(ens, dkg2.EnShare)
	}

	for proposerId := 0; proposerId < nr; proposerId++ {
		sig, err := rb.generateSIG(uint32(proposerId))
		if err != nil {
			t.Fatal("generate sig fail. err:", err)
		}

		// Verification
		M, err := tmpGetRBM(rb.statedb, rb.epochId)
		if err != nil {
			t.Fatal("getRBM error, err:", err)
		}

		m := new(big.Int).SetBytes(M)
		var gpkshare bn256.G2

		for id := 0; id < nr; id++ {
			gpkshare.Add(&gpkshare, dkg1s[id].Commit[proposerId])
		}

		mG := new(bn256.G1).ScalarBaseMult(m)
		pair1 := bn256.Pair(sig.GSignShare, hbase)
		pair2 := bn256.Pair(mG, &gpkshare)
		if pair1.String() != pair2.String() {
			t.Fatal("verify sig result pair fail")
		}
	}
}

func tmpGetEnsFunc(db vm.StateDB, epochId uint64, proposerId uint32) ([]*bn256.G1, error) {
	return ens[proposerId], nil
}

func tmpGetRBM(db vm.StateDB, epochId uint64) ([]byte, error) {
	epochIdBigInt := big.NewInt(int64(epochId + 1))
	buf := epochIdBigInt.Bytes()
	return crypto.Keccak256(buf), nil
}

func tmpGetCji(db vm.StateDB, epochId uint64, proposerId uint32) ([]*bn256.G2, error) {
	ret := make([]*bn256.G2, len(commit[proposerId]))
	for i, _ := range commit[proposerId] {
		ret[i] = &commit[proposerId][i]
	}

	return ret, nil
}
