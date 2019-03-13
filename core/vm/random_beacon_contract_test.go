package vm

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/wanchain/go-wanchain/common"
	"github.com/wanchain/go-wanchain/core/state"
	"github.com/wanchain/go-wanchain/core/types"
	"github.com/wanchain/go-wanchain/crypto"
	"github.com/wanchain/go-wanchain/ethdb"
	"github.com/wanchain/go-wanchain/params"
	"github.com/wanchain/go-wanchain/pos"
	"github.com/wanchain/go-wanchain/rlp"
	"github.com/wanchain/pos/cloudflare"
	"github.com/wanchain/pos/wanpos_crypto"
	"math/big"
	mrand "math/rand"
	"testing"
	"time"
)

type CTStateDB struct {
}

func (CTStateDB) CreateAccount(common.Address) {}

func (CTStateDB) SubBalance(common.Address, *big.Int) {}
func (CTStateDB) AddBalance(addr common.Address, pval *big.Int) {

}
func (CTStateDB) GetBalance(addr common.Address) *big.Int {
	defaulVal, _ := new(big.Int).SetString("10000000000000000000", 10)
	return defaulVal
}
func (CTStateDB) GetNonce(common.Address) uint64                                         { return 0 }
func (CTStateDB) SetNonce(common.Address, uint64)                                        {}
func (CTStateDB) GetCodeHash(common.Address) common.Hash                                 { return common.Hash{} }
func (CTStateDB) GetCode(common.Address) []byte                                          { return nil }
func (CTStateDB) SetCode(common.Address, []byte)                                         {}
func (CTStateDB) GetCodeSize(common.Address) int                                         { return 0 }
func (CTStateDB) AddRefund(*big.Int)                                                     {}
func (CTStateDB) GetRefund() *big.Int                                                    { return nil }
func (CTStateDB) GetState(common.Address, common.Hash) common.Hash                       { return common.Hash{} }
func (CTStateDB) SetState(common.Address, common.Hash, common.Hash)                      {}
func (CTStateDB) Suicide(common.Address) bool                                            { return false }
func (CTStateDB) HasSuicided(common.Address) bool                                        { return false }
func (CTStateDB) Exist(common.Address) bool                                              { return false }
func (CTStateDB) Empty(common.Address) bool                                              { return false }
func (CTStateDB) RevertToSnapshot(int)                                                   {}
func (CTStateDB) Snapshot() int                                                          { return 0 }
func (CTStateDB) AddLog(*types.Log)                                                      {}
func (CTStateDB) AddPreimage(common.Hash, []byte)                                        {}
func (CTStateDB) ForEachStorage(common.Address, func(common.Hash, common.Hash) bool)     {}
func (CTStateDB) ForEachStorageByteArray(common.Address, func(common.Hash, []byte) bool) {}

var (
	rbepochId = uint64(0)
	rbdb = make(map[common.Hash][]byte)
	rbgroupdb = make(map[uint64][]bn256.G1)
	rbranddb = make(map[uint64]*big.Int)
)

func (CTStateDB) GetStateByteArray(addr common.Address, hs common.Hash) []byte {
	return rbdb[hs]
}

func (CTStateDB) SetStateByteArray(addr common.Address, hs common.Hash, data []byte) {
	rbdb[hs] = data
}

type dummyCtRef struct {
	calledForEach bool
}

func (dummyCtRef) ReturnGas(*big.Int)          {}
func (dummyCtRef) Address() common.Address     { return common.Address{} }
func (dummyCtRef) Value() *big.Int             { return new(big.Int) }
func (dummyCtRef) SetCode(common.Hash, []byte) {}
func (d *dummyCtRef) ForEachStorage(callback func(key, value common.Hash) bool) {
	d.calledForEach = true
}
func (d *dummyCtRef) SubBalance(amount *big.Int) {}
func (d *dummyCtRef) AddBalance(amount *big.Int) {}
func (d *dummyCtRef) SetBalance(*big.Int)        {}
func (d *dummyCtRef) SetNonce(uint64)            {}
func (d *dummyCtRef) Balance() *big.Int          { return new(big.Int) }

type dummyCtDB struct {
	CTStateDB
	ref *dummyCtRef
}

var (
	nr = 21
	thres = pos.Cfg().PolymDegree + 1

	db, _      = ethdb.NewMemDatabase()
	statedb, _ = state.New(common.Hash{}, state.NewDatabase(db))
	ref = &dummyCtRef{}
	evm = NewEVM(Context{Time:big.NewInt(time.Now().Unix())}, dummyCtDB{ref: ref}, params.TestChainConfig, Config{EnableJit: false, ForceJit: false})

	rbcontract = &RandomBeaconContract{}
	rbcontractParam = &Contract{}

	pubs, pris, hpubs = generateKeyPairs()
	//s, sshare, enshare, commit, proof := prepareDkg(pubs, pris, hpubs)
	_, _, enshareA, commitA, proofA = prepareDkg(pubs, pris, hpubs)
)

// pubs,pris,hashPubs
func generateKeyPairs() ([]bn256.G1, []big.Int, []big.Int) {
	Pubkey := make([]bn256.G1, nr)
	Prikey := make([]big.Int, nr)

	for i := 0; i < nr; i++ {
		Pri, Pub, err := bn256.RandomG1(rand.Reader)
		if err != nil {
			println(err)
		}
		Prikey[i] = *Pri
		Pubkey[i] = *Pub
	}
	x := make([]big.Int, nr)
	for i := 0; i < nr; i++ {
		x[i].SetBytes(GetPolynomialX(&Pubkey[i], uint32(i)))
		x[i].Mod(&x[i], bn256.Order)
	}

	return Pubkey, Prikey, x
}

func prepareDkg(Pubkey []bn256.G1, Prikey []big.Int, x []big.Int) ([]*big.Int, [][]big.Int, [][]*bn256.G1, [][]*bn256.G2, [][]wanpos.DLEQproof) {
	// Each of random propoer generates a random si
	s := make([]*big.Int, nr)

	source := mrand.NewSource(int64(nr))
	r := mrand.New(source)

	for i := 0; i < nr; i++ {
		s[i], _ = rand.Int(r, bn256.Order)
	}

	// Each random propoer conducts the shamir secret sharing process
	poly := make([]wanpos.Polynomial, nr)

	sshare := make([][]big.Int, nr, nr)

	for i := 0; i < nr; i++ {
		sshare[i] = make([]big.Int, nr, nr)
		poly[i] = wanpos.RandPoly(int(thres-1), *s[i])	// fi(x), set si as its constant term
		for j := 0; j < nr; j++ {
			sshare[i][j], _ = wanpos.EvaluatePoly(poly[i], &x[j], int(thres-1)) // share for j is fi(x) evaluation result on x[j]=Hash(Pub[j])
		}
	}

	// Encrypt the secret share, i.e. mutiply with the receiver's public key
	enshare := make([][]*bn256.G1, nr, nr)
	for i := 0; i < nr; i++ {
		enshare[i] = make([]*bn256.G1, nr, nr)
		for j := 0; j < nr; j++ { // enshare[j] = sshare[j]*Pub[j], it is a point on ECC
			enshare[i][j] = new(bn256.G1).ScalarMult(&Pubkey[j], &sshare[i][j])
		}
	}

	// Make commitment for the secret share, i.e. multiply with the generator of G2
	commit := make([][]*bn256.G2, nr, nr)
	for i := 0; i < nr; i++ {
		commit[i] = make([]*bn256.G2, nr, nr)
		for j := 0; j < nr; j++ { // commit[j] = sshare[j] * G2
			commit[i][j] = new(bn256.G2).ScalarBaseMult(&sshare[i][j])
		}
	}

	// generate DLEQ proof
	proof := make([][]wanpos.DLEQproof, nr, nr)
	for i := 0; i < nr; i++ {
		proof[i] = make([]wanpos.DLEQproof, nr, nr)
		for j := 0; j < nr; j++ { // proof = (a1, a2, z)
			proof[i][j] = wanpos.DLEQ(Pubkey[j], *hbase, &sshare[i][j])
		}
	}

	return s, sshare, enshare, commit, proof
}

func prepareSig(Prikey []big.Int, enshare [][]*bn256.G1) ([]*bn256.G1)  {
	gskshare := make([]bn256.G1, nr)

	for i := 0; i < nr; i++ {

		gskshare[i].ScalarBaseMult(big.NewInt(int64(0))) //set zero

		skinver := new(big.Int).ModInverse(&Prikey[i], bn256.Order) // sk^-1

		for j := 0; j < nr; j++ {
			temp := new(bn256.G1).ScalarMult(enshare[j][i], skinver)
			gskshare[i].Add(&gskshare[i], temp) // gskshare[i] = (sk^-1)*(enshare[1][i]+...+enshare[Nr][i])
		}
	}

	M, err := getRBMVar(statedb, rbepochId)
	if err != nil {
		fmt.Println("get rbm error id:%u", rbepochId)
	}
	m := new(big.Int).SetBytes(M)

	// Compute signature share
	gsigshare := make([]*bn256.G1, nr)
	for i := 0; i < nr; i++ { // signature share = M * secret key share
		gsigshare[i] = new(bn256.G1).ScalarMult(&gskshare[i], m)
	}
	return gsigshare
}

func getRBProposerGroupMock(epochId uint64) []bn256.G1 {
	return rbgroupdb[epochId]
}


func getRBMMock(db StateDB, epochId uint64) ([]byte, error) {
	nextEpochId := big.NewInt(int64(epochId + 1))
	preRandom := rbranddb[epochId]
	if preRandom == nil {
		return nil, errors.New("getRBMMock")
	}

	//buf := make([]byte, len(nextEpochId.Bytes()) + len(preRandom.Bytes()))
	buf := nextEpochId.Bytes()
	buf = append(buf, preRandom.Bytes()...)
	rt := crypto.Keccak256(buf)

	rbranddb[epochId + 1] = new(big.Int).SetBytes(rt)

	return rt, nil
}


func isValidEpochStageMock(epochId uint64, stage int, time uint64) bool {
	return true
}
func isInRandomGroupMock(pks []bn256.G1, epochId uint64, proposerId uint32, address common.Address) bool {
	return true
}


func intrinsicGas(data []byte, contractCreation, homestead bool) *big.Int {
	igas := new(big.Int)
	if contractCreation && homestead {
		igas.SetUint64(params.TxGasContractCreation)
	} else {
		igas.SetUint64(params.TxGas)
	}
	if len(data) > 0 {
		var nz int64
		for _, byt := range data {
			if byt != 0 {
				nz++
			}
		}
		m := big.NewInt(nz)
		m.Mul(m, new(big.Int).SetUint64(params.TxDataNonZeroGas))
		igas.Add(igas, m)
		m.SetInt64(int64(len(data)) - nz)
		m.Mul(m, new(big.Int).SetUint64(params.TxDataZeroGas))
		igas.Add(igas, m)
	}
	return igas
}

// test cases runs in testMain
func TestMain(m *testing.M) {
	rbranddb[0] = big.NewInt(1)
	getRBProposerGroupVar = getRBProposerGroupMock
	getRBMVar = getRBMMock
	isValidEpochStageVar = isValidEpochStageMock
	isInRandomGroupVar = isInRandomGroupMock
	//println("rb test begin")
	m.Run()
	println("rb test end")
}

func show(v interface{}) {
	println(fmt.Sprintf("%v", v))
}

func buildDkg1(payloadBytes [] byte) []byte {
	payload := make([]byte, 4+len(payloadBytes))
	copy(payload, GetDkg1Id())
	copy(payload[4:], payloadBytes)
	return payload
}
func buildDkg2(payloadBytes [] byte) []byte {
	payload := make([]byte, 4+len(payloadBytes))
	copy(payload, GetDkg2Id())
	copy(payload[4:], payloadBytes)
	return payload
}
func buildSig(payloadBytes [] byte) []byte {
	payload := make([]byte, 4+len(payloadBytes))
	copy(payload, GetSigshareId())
	copy(payload[4:], payloadBytes)
	return payload
}

func TestRBDkg1(t *testing.T) {
	rbgroupdb[rbepochId] = pubs

	for i := 0; i < nr; i++ {
		var dkgParam RbDKG1TxPayload
		dkgParam.EpochId = rbepochId
		dkgParam.ProposerId = uint32(i)
		dkgParam.Commit = commitA[i]

		dkg1 := Dkg1ToDkg1Flat(&dkgParam)
		cijBytes1, _ := rlp.EncodeToBytes(dkg1.Commit)

		payloadBytes, _ := rlp.EncodeToBytes(dkg1)


		payload := buildDkg1(payloadBytes)

		hashCij := GetRBKeyHash(kindCij, dkgParam.EpochId, dkgParam.ProposerId)

		_, err := rbcontract.Run(payload, rbcontractParam, evm)
		if err != nil {
			t.Error(err)
		}

		cijBytes2 := evm.StateDB.GetStateByteArray(randomBeaconPrecompileAddr, *hashCij)

		if !bytes.Equal(cijBytes1, cijBytes2) {
			println("cij error")
		}
	}
}
func TestRBDkg2(t *testing.T) {
	rbgroupdb[rbepochId] = pubs
	TestRBDkg1(t)

	for i := 0; i < nr; i++ {
		var dkgParam RbDKG2TxPayload
		dkgParam.EpochId = rbepochId
		dkgParam.ProposerId = uint32(i)
		dkgParam.Enshare = enshareA[i]
		dkgParam.Proof = proofA[i]

		dkg1 := Dkg2ToDkg2Flat(&dkgParam)
		ensBytes1, _ := rlp.EncodeToBytes(dkg1.Enshare)

		payloadBytes, _ := rlp.EncodeToBytes(dkg1)


		payload := buildDkg2(payloadBytes)

		hashEns := GetRBKeyHash(kindEns, dkgParam.EpochId, dkgParam.ProposerId)

		_, err := rbcontract.Run(payload, rbcontractParam, evm)
		if err != nil {
			t.Error(err)
		}

		ensBytes2 := evm.StateDB.GetStateByteArray(randomBeaconPrecompileAddr, *hashEns)

		if !bytes.Equal(ensBytes1, ensBytes2) {
			println("cij error")
		}
	}
}

func TestRBSig(t *testing.T)  {
	TestRBDkg2(t)
	gsigshareA := prepareSig(pris, enshareA)
	for i := 0; i < nr; i++ {
		var sigshareParam RbSIGTxPayload
		sigshareParam.EpochId = rbepochId
		sigshareParam.ProposerId = uint32(i)
		sigshareParam.Gsigshare = gsigshareA[i]

		payloadBytes, _ := rlp.EncodeToBytes(sigshareParam)
		payload := buildSig(payloadBytes)
		hash := GetRBKeyHash(sigshareId[:], sigshareParam.EpochId, sigshareParam.ProposerId)

		_, err := rbcontract.Run(payload, rbcontractParam, evm)
		if err != nil {
			t.Error(err)
		}
		payloadBytes2 := evm.StateDB.GetStateByteArray(randomBeaconPrecompileAddr, *hash)

		if !bytes.Equal(payloadBytes, payloadBytes2) {
			println("error")
		}
		sigshareParam2, err := GetSig(evm.StateDB, rbepochId, uint32(i))
		println (sigshareParam2)
	}
}

func TestValidPosTx(t *testing.T) {
	rbgroupdb[rbepochId] = pubs

	gasPrice := big.NewInt(1800000000000)
	txAmount := big.NewInt(0)
	gasLimit := big.NewInt(1000000)

	for i := 0; i < nr; i++ {
		var dkgParam RbDKG1TxPayload
		dkgParam.EpochId = rbepochId
		dkgParam.ProposerId = uint32(i)
		dkgParam.Commit = commitA[i]

		dkg1 := Dkg1ToDkg1Flat(&dkgParam)
		payloadBytes, _ := rlp.EncodeToBytes(dkg1)
		payload := buildDkg1(payloadBytes)

		intrGas := intrinsicGas(payload, false, true)
		err := ValidPosTx(evm.StateDB, contract.CallerAddress, payload, gasPrice, intrGas, txAmount, gasLimit)
		if err != nil {
			t.Error("verify pos tx fail. err:", err)
		}

		_, err = rbcontract.Run(payload, rbcontractParam, evm)
		if err != nil {
			t.Error("rb contract run fail. err:", err)
		}
	}

	for i := 0; i < nr; i++ {
		var dkgParam RbDKG2TxPayload
		dkgParam.EpochId = rbepochId
		dkgParam.ProposerId = uint32(i)
		dkgParam.Enshare = enshareA[i]
		dkgParam.Proof = proofA[i]

		dkg1 := Dkg2ToDkg2Flat(&dkgParam)
		payloadBytes, _ := rlp.EncodeToBytes(dkg1)
		payload := buildDkg2(payloadBytes)

		intrGas := intrinsicGas(payload, false, true)
		err := ValidPosTx(evm.StateDB, contract.CallerAddress, payload, gasPrice, intrGas, txAmount, gasLimit)
		if err != nil {
			t.Error("verify pos tx fail. err:", err)
		}

		_, err = rbcontract.Run(payload, rbcontractParam, evm)
		if err != nil {
			t.Error("rb contract run fail. err:", err)
		}

	}

	gsigshareA := prepareSig(pris, enshareA)
	for i := 0; i < nr; i++ {
		var sigshareParam RbSIGTxPayload
		sigshareParam.EpochId = rbepochId
		sigshareParam.ProposerId = uint32(i)
		sigshareParam.Gsigshare = gsigshareA[i]

		payloadBytes, _ := rlp.EncodeToBytes(sigshareParam)
		payload := buildSig(payloadBytes)

		intrGas := intrinsicGas(payload, false, true)
		err := ValidPosTx(evm.StateDB, contract.CallerAddress, payload, gasPrice, intrGas, txAmount, gasLimit)
		if err != nil {
			t.Error("verify pos tx fail. err:", err)
		}

		_, err = rbcontract.Run(payload, rbcontractParam, evm)
		if err != nil {
			t.Error("rb contract run fail. err:", err)
		}

	}
}

func TestGetRBStage(t *testing.T) {
	datas := [][]int{
		{0, RbDkg1Stage, 0, int(2*pos.K-1)},
		{9, RbDkg1Stage, 9, int(2*pos.K-10)},
		{19, RbDkg1Stage, 19, 0},
		{20, RbDkg1ConfirmStage, 0, int(2*pos.K-1)},
		{29, RbDkg1ConfirmStage, 9, int(2*pos.K-10)},
		{39, RbDkg1ConfirmStage, 19, 0},
		{40, RbDkg2Stage, 0, int(2*pos.K-1)},
		{49, RbDkg2Stage, 9, int(2*pos.K-10)},
		{59, RbDkg2Stage, 19, 0},
		{60, RbDkg2ConfirmStage, 0, int(2*pos.K-1)},
		{69, RbDkg2ConfirmStage, 9, int(2*pos.K-10)},
		{79, RbDkg2ConfirmStage, 19, 0},
		{80, RbSignStage, 0, int(2*pos.K-1)},
		{89, RbSignStage, 9, int(2*pos.K-10)},
		{99, RbSignStage, 19, 0},
		{100, RbSignConfirmStage, 0, int(2*pos.K-1)},
		{109, RbSignConfirmStage, 9, int(2*pos.K-10)},
		{119, RbSignConfirmStage, 19, 0},
	}

	for i, _ := range datas {
		stage, elapsed, left := GetRBStage(uint64(datas[i][0]))
		if datas[i][1] != stage || datas[i][2] != elapsed || datas[i][3] != left {
			t.Error("expect(stage:", datas[i][1], ", elapsed:", datas[i][2], ", left:", datas[i][3],
				")    acture(stage:", stage, ", elapsed:", elapsed, ", left:", left, ")")
		}
	}
}

