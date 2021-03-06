package vm

import (
	"bytes"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/wanchain/go-wanchain/rlp"
	"github.com/wanchain/go-wanchain/pos/uleaderselection"

	"github.com/wanchain/go-wanchain/pos/posconfig"
	"github.com/wanchain/go-wanchain/pos/posdb"
	"github.com/wanchain/go-wanchain/pos/util"
	"github.com/wanchain/go-wanchain/pos/util/convert"

	"github.com/wanchain/go-wanchain/functrace"

	"github.com/wanchain/go-wanchain/common"
	"github.com/wanchain/go-wanchain/crypto"
	"github.com/wanchain/go-wanchain/log"

	"github.com/wanchain/go-wanchain/accounts/abi"
	"github.com/wanchain/go-wanchain/core/types"
)

const (
	SlotLeaderStag1 = "slotLeaderStag1"
	SlotLeaderStag2 = "slotLeaderStag2"

	SlotLeaderStag1Indexes = "slotLeaderStag1Indexes"
	SlotLeaderStag2Indexes = "slotLeaderStag2Indexes"
)

var (
	slotLeaderSCDef = `[
			{
				"constant": false,
				"type": "function",
				"inputs": [
					{
						"name": "data",
						"type": "string"
					}
				],
				"name": "slotLeaderStage1MiSave",
				"outputs": [
					{
						"name": "data",
						"type": "string"
					}
				]
			},
			{
				"constant": false,
				"type": "function",
				"inputs": [
					{
						"name": "data",
						"type": "string"
					}
				],
				"name": "slotLeaderStage2InfoSave",
				"outputs": [
					{
						"name": "data",	
						"type": "string"
					}
				]
			}
		]`
	slotLeaderAbi, errSlotLeaderSCInit = abi.JSON(strings.NewReader(slotLeaderSCDef))
	stgOneIdArr, stgTwoIdArr           [4]byte

	scCallTimes = "SLOT_LEADER_SC_CALL_TIMES"
)

var (
	ErrEpochID                         = errors.New("EpochID is not valid")
	ErrIllegalSender                   = errors.New("sender is not in epoch leaders ")
	ErrInvalidLocalPublicKey           = errors.New("getLocalPublicKey error, do not found unlock address")
	ErrInvalidPreEpochLeaders          = errors.New("can not found pre epoch leaders return epoch 0")
	ErrInvalidGenesisPk                = errors.New("invalid GenesisPK hex string")
	ErrSlotLeaderGroupNotReady         = errors.New("slot leaders group not ready")
	ErrSlotIDOutOfRange                = errors.New("slot id index out of range")
	ErrPkNotInCurrentEpochLeadersGroup = errors.New("local public key is not in current Epoch leaders")
	ErrInvalidRandom                   = errors.New("get random message error")
	ErrNotOnCurve                      = errors.New("not on curve")
	ErrTx1AndTx2NotConsistent          = errors.New("stageOneMi is not equal sageTwoAlphaPki")
	ErrEpochLeaderNotReady             = errors.New("epoch leaders are not ready")
	ErrNoTx2TransInDB                  = errors.New("tx2 is not in db")
	ErrCollectTxData                   = errors.New("collect tx data error")
	ErrRlpUnpackErr                    = errors.New("RlpUnpackDataForTx error")
	ErrNoTx1TransInDB                  = errors.New("GetStg1StateDbInfo: Found not data of key")
	ErrVerifyStg1Data                  = errors.New("stg1 data get from StateDb verified failed")
	ErrDleqProof                       = errors.New("VerifyDleqProof false")
	ErrInvalidTxLen                    = errors.New("len(mi)==0 or len(alphaPkis) is not right")
	ErrInvalidTx1Range                 = errors.New("slot leader tx1 is not in invalid range")
	ErrInvalidTx2Range                 = errors.New("slot leader tx2 is not in invalid range")
)

func init() {
	if errSlotLeaderSCInit != nil {
		panic("err in slot leader sc initialize :" + errSlotLeaderSCInit.Error())
	}

	stgOneIdArr, _ = GetStage1FunctionID(slotLeaderSCDef)
	stgTwoIdArr, _ = GetStage2FunctionID(slotLeaderSCDef)
}

type slotLeaderSC struct {
}

func (c *slotLeaderSC) RequiredGas(input []byte) uint64 {
	return 0
}

func (c *slotLeaderSC) Run(in []byte, contract *Contract, evm *EVM) ([]byte, error) {
	functrace.Enter()
	log.Debug("slotLeaderSC run is called")

	if len(in) < 4 {
		return nil, errParameters
	}

	var methodId [4]byte
	copy(methodId[:], in[:4])
	var from common.Address
	from = contract.CallerAddress

	if methodId == stgOneIdArr {
		err := c.validTxStg1ByData(evm.StateDB, from, in[:])
		if err != nil {
			log.Error("slotLeaderSC:Run:validTxStg1ByData", "from", from)
			return nil, err
		}
		return c.handleStgOne(in[:], contract, evm) //Do not use [4:] because it has do it in function
	} else if methodId == stgTwoIdArr {
		err := c.validTxStg2ByData(evm.StateDB, from, in[:])
		if err != nil {
			log.Error("slotLeaderSC:Run:validTxStg2ByData", "from", from)
			return nil, err
		}
		return c.handleStgTwo(in[:], contract, evm)
	}

	functrace.Exit()
	return nil, errMethodId
}

func (c *slotLeaderSC) ValidTx(stateDB StateDB, signer types.Signer, tx *types.Transaction) error {
	var methodId [4]byte
	copy(methodId[:], tx.Data()[:4])

	if methodId == stgOneIdArr {
		return c.validTxStg1(stateDB, signer, tx)
	} else if methodId == stgTwoIdArr {
		return c.validTxStg2(stateDB, signer, tx)
	} else {
		return errMethodId
	}
	return nil
}

func (c *slotLeaderSC) handleStgOne(in []byte, contract *Contract, evm *EVM) ([]byte, error) {
	log.Debug("slotLeaderSC handleStgOne is called")

	epochIDBuf, selfIndexBuf, err := RlpGetStage1IDFromTx(in)
	if err != nil {
		return nil, err
	}

	if !isInValidStage(convert.BytesToUint64(epochIDBuf), evm, posconfig.Sma1Start, posconfig.Sma1End) {

		log.Warn("epid cal","",convert.BytesToUint64(epochIDBuf))
		log.Warn("Not in range handleStgOne", "hash", crypto.Keccak256Hash(in).Hex())
		return nil, ErrInvalidTx1Range
	}

	keyHash := GetSlotLeaderStage1KeyHash(epochIDBuf, selfIndexBuf)

	evm.StateDB.SetStateByteArray(slotLeaderPrecompileAddr, keyHash, in)

	err = updateSlotLeaderStageIndex(evm, epochIDBuf, SlotLeaderStag1Indexes, convert.BytesToUint64(selfIndexBuf))

	if err != nil {
		return nil, err
	}

	addSlotScCallTimes(convert.BytesToUint64(epochIDBuf))

	log.Debug(fmt.Sprintf("handleStgOne save data addr:%s, key:%s, data len:%d", slotLeaderPrecompileAddr.Hex(),
		keyHash.Hex(), len(in)))
	log.Debug("handleStgOne save", "epochID", convert.BytesToUint64(epochIDBuf), "selfIndex",
		convert.BytesToUint64(selfIndexBuf))

	return nil, nil
}

func (c *slotLeaderSC) handleStgTwo(in []byte, contract *Contract, evm *EVM) ([]byte, error) {

	epochIDBuf, selfIndexBuf, err := RlpGetStage2IDFromTx(in)
	if err != nil {
		return nil, err
	}

	if !isInValidStage(convert.BytesToUint64(epochIDBuf), evm, posconfig.Sma2Start, posconfig.Sma2End) {
		log.Warn("Not in range handleStgTwo", "hash", crypto.Keccak256Hash(in).Hex())
		return nil, ErrInvalidTx2Range
	}

	keyHash := GetSlotLeaderStage2KeyHash(epochIDBuf, selfIndexBuf)

	evm.StateDB.SetStateByteArray(slotLeaderPrecompileAddr, keyHash, in)

	err = updateSlotLeaderStageIndex(evm, epochIDBuf, SlotLeaderStag2Indexes, convert.BytesToUint64(selfIndexBuf))

	if err != nil {
		return nil, err
	}
	addSlotScCallTimes(convert.BytesToUint64(epochIDBuf))

	log.Debug(fmt.Sprintf("handleStgTwo save data addr:%s, key:%s, data len:%d", slotLeaderPrecompileAddr.Hex(),
		keyHash.Hex(), len(in)))
	log.Debug("handleStgTwo save", "epochID", convert.BytesToUint64(epochIDBuf), "selfIndex",
		convert.BytesToUint64(selfIndexBuf))

	functrace.Exit()
	return nil, nil
}

func (c *slotLeaderSC) validTxStg1(stateDB StateDB, signer types.Signer, tx *types.Transaction) error {
	sender, err := signer.Sender(tx)
	if err != nil {
		return err
	}

	return c.validTxStg1ByData(stateDB, sender, tx.Data())
}

func (c *slotLeaderSC) validTxStg1ByData(stateDB StateDB, from common.Address, payload []byte) error {

	epochIDBuf, _, err := RlpGetStage1IDFromTx(payload[:])
	if err != nil {
		log.Error("validTxStg1 failed")
		return err
	}

	if !InEpochLeadersOrNotByAddress(convert.BytesToUint64(epochIDBuf), from) {
		log.Error("validTxStg1 failed")
		return ErrIllegalSender
	}

	//log.Info("validTxStg1 success")
	return nil
}

func (c *slotLeaderSC) validTxStg2ByData(stateDB StateDB, from common.Address, payload []byte) error {
	epochID, selfIndex, _, alphaPkis, proofs, err := RlpUnpackStage2DataForTx(payload[:])
	if err != nil {
		log.Error("validTxStg2:RlpUnpackStage2DataForTx failed")
		return err
	}

	if !InEpochLeadersOrNotByAddress(epochID, from) {
		log.Error("validTxStg2:InEpochLeadersOrNotByAddress failed")
		return ErrIllegalSender
	}

	//log.Info("validTxStg2 success")

	mi, err := GetStg1StateDbInfo(stateDB, epochID, selfIndex)
	if err != nil {
		log.Error("validTxStg2", "GetStg1StateDbInfo error", err.Error())
		return err
	}

	//mi
	if len(mi) == 0 || len(alphaPkis) != posconfig.EpochLeaderCount {
		log.Error("validTxStg2", "len(mi)==0 or len(alphaPkis) not equal", len(alphaPkis))
		return ErrInvalidTxLen
	}
	if !util.PkEqual(crypto.ToECDSAPub(mi), alphaPkis[selfIndex]) {
		log.Error("validTxStg2", "mi is not equal alphaPkis[index]", selfIndex)
		return ErrTx1AndTx2NotConsistent
	}
	//Dleq

	buff := util.GetEpocherInst().GetEpochLeaders(epochID)
	epochLeaders := make([]*ecdsa.PublicKey, len(buff))
	for i := 0; i < len(buff); i++ {
		epochLeaders[i] = crypto.ToECDSAPub(buff[i])
	}

	if !(uleaderselection.VerifyDleqProof(epochLeaders, alphaPkis, proofs)) {
		log.Error("validTxStg2", "VerifyDleqProof false self Index", selfIndex)
		return ErrDleqProof
	}
	return nil
}

func (c *slotLeaderSC) validTxStg2(stateDB StateDB, signer types.Signer, tx *types.Transaction) error {
	sender, err := signer.Sender(tx)
	if err != nil {
		return err
	}
	return c.validTxStg2ByData(stateDB, sender, tx.Data())
}

// GetSlotLeaderStage2KeyHash use to get SlotLeader Stage 1 KeyHash by epochid and selfindex
func GetSlotLeaderStage2KeyHash(epochID, selfIndex []byte) common.Hash {
	return getSlotLeaderStageKeyHash(epochID, selfIndex, SlotLeaderStag2)
}

func GetSlotLeaderStage2IndexesKeyHash(epochID []byte) common.Hash {
	return getSlotLeaderStageIndexesKeyHash(epochID, SlotLeaderStag2Indexes)
}

// GetSlotLeaderSCAddress can get the precompile contract address
func GetSlotLeaderSCAddress() common.Address {
	return slotLeaderPrecompileAddr
}

// GetSlotLeaderScAbiString can get the precompile contract Define string
func GetSlotLeaderScAbiString() string {
	return slotLeaderSCDef
}

// GetSlotScCallTimes can get this precompile contract called times
func GetSlotScCallTimes(epochID uint64) uint64 {
	buf, err := posdb.GetDb().Get(epochID, scCallTimes)
	if err != nil {
		return 0
	} else {
		return convert.BytesToUint64(buf)
	}
}

// GetSlotLeaderStage1KeyHash use to get SlotLeader Stage 1 KeyHash by epoch id and self index

func GetSlotLeaderStage1KeyHash(epochID, selfIndex []byte) common.Hash {
	return getSlotLeaderStageKeyHash(epochID, selfIndex, SlotLeaderStag1)
}

func GetStage1FunctionID(abiString string) ([4]byte, error) {
	var slotStage1ID [4]byte

	abi, err := util.GetAbi(abiString)
	if err != nil {
		return slotStage1ID, err
	}

	copy(slotStage1ID[:], abi.Methods["slotLeaderStage1MiSave"].Id())

	return slotStage1ID, nil
}

func GetStage2FunctionID(abiString string) ([4]byte, error) {
	var slotStage2ID [4]byte

	abi, err := util.GetAbi(abiString)
	if err != nil {
		return slotStage2ID, err
	}

	copy(slotStage2ID[:], abi.Methods["slotLeaderStage2InfoSave"].Id())

	return slotStage2ID, nil
}

// PackStage1Data can pack stage1 data into abi []byte for tx payload
func PackStage1Data(input []byte, abiString string) ([]byte, error) {
	id, err := GetStage1FunctionID(abiString)
	outBuf := make([]byte, len(id)+len(input))
	copy(outBuf[:4], id[:])
	copy(outBuf[4:], input[:])
	return outBuf, err
}

func InEpochLeadersOrNotByAddress(epochID uint64, senderAddress common.Address) bool {
	epochLeaders := util.GetEpocherInst().GetEpochLeaders(epochID)
	if len(epochLeaders) != posconfig.EpochLeaderCount {
		log.Warn("epoch leader is not ready use epoch 0 at InEpochLeadersOrNotByAddress", "epochID", epochID)
		epochLeaders = util.GetEpocherInst().GetEpochLeaders(0)
	}

	for i := 0; i < len(epochLeaders); i++ {
		if crypto.PubkeyToAddress(*crypto.ToECDSAPub(epochLeaders[i])).Hex() == senderAddress.Hex() {
			return true
		}
	}

	return false
}

type stage1Data struct {
	EpochID    uint64
	SelfIndex  uint64
	MiCompress []byte
}

// RlpPackStage1DataForTx
func RlpPackStage1DataForTx(epochID uint64, selfIndex uint64, mi *ecdsa.PublicKey, abiString string) ([]byte, error) {
	pkBuf, err := util.CompressPk(mi)
	if err != nil {
		return nil, err
	}
	data := &stage1Data{
		EpochID:    epochID,
		SelfIndex:  selfIndex,
		MiCompress: pkBuf,
	}

	buf, err := rlp.EncodeToBytes(data)
	if err != nil {
		return nil, err
	}

	return PackStage1Data(buf, abiString)
}

// RlpUnpackStage1DataForTx
func RlpUnpackStage1DataForTx(input []byte) (epochID uint64, selfIndex uint64, mi *ecdsa.PublicKey, err error) {
	var data *stage1Data

	buf := input[4:]

	err = rlp.DecodeBytes(buf, &data)
	if err != nil {
		return
	}

	epochID = data.EpochID
	selfIndex = data.SelfIndex
	mi, err = util.UncompressPk(data.MiCompress)
	return
}

// RlpGetStage1IDFromTx
func RlpGetStage1IDFromTx(input []byte) (epochIDBuf []byte, selfIndexBuf []byte, err error) {
	var data *stage1Data

	buf := input[4:]

	err = rlp.DecodeBytes(buf, &data)
	if err != nil {
		return
	}
	epochIDBuf = convert.Uint64ToBytes(data.EpochID)
	selfIndexBuf = convert.Uint64ToBytes(data.SelfIndex)
	return
}

type stage2Data struct {
	EpochID   uint64
	SelfIndex uint64
	SelfPk    []byte
	AlphaPki  [][]byte
	Proof     []*big.Int
}

func RlpPackStage2DataForTx(epochID uint64, selfIndex uint64, selfPK *ecdsa.PublicKey, alphaPki []*ecdsa.PublicKey,
	proof []*big.Int, abiString string) ([]byte, error) {
	pk, err := util.CompressPk(selfPK)
	if err != nil {
		return nil, err
	}

	pks := make([][]byte, len(alphaPki))
	for i := 0; i < len(alphaPki); i++ {
		pks[i], err = util.CompressPk(alphaPki[i])
		if err != nil {
			return nil, err
		}
	}

	data := &stage2Data{
		EpochID:   epochID,
		SelfIndex: selfIndex,
		SelfPk:    pk,
		AlphaPki:  pks,
		Proof:     proof,
	}

	buf, err := rlp.EncodeToBytes(data)
	if err != nil {
		return nil, err
	}

	id, err := GetStage2FunctionID(abiString)
	if err != nil {
		return nil, err
	}

	outBuf := make([]byte, len(id)+len(buf))
	copy(outBuf[:4], id[:])
	copy(outBuf[4:], buf[:])

	return outBuf, nil
}

func RlpUnpackStage2DataForTx(input []byte) (epochID uint64, selfIndex uint64, selfPK *ecdsa.PublicKey,
	alphaPki []*ecdsa.PublicKey, proof []*big.Int, err error) {
	inputBuf := input[4:]

	var data stage2Data
	err = rlp.DecodeBytes(inputBuf, &data)
	if err != nil {
		return
	}

	epochID = data.EpochID
	selfIndex = data.SelfIndex
	selfPK, err = util.UncompressPk(data.SelfPk)
	if err != nil {
		return
	}

	alphaPki = make([]*ecdsa.PublicKey, len(data.AlphaPki))
	for i := 0; i < len(data.AlphaPki); i++ {
		alphaPki[i], err = util.UncompressPk(data.AlphaPki[i])
		if err != nil {
			return
		}
	}

	proof = data.Proof
	return
}

func RlpGetStage2IDFromTx(input []byte) (epochIDBuf []byte, selfIndexBuf []byte, err error) {
	inputBuf := input[4:]

	var data stage2Data
	err = rlp.DecodeBytes(inputBuf, &data)
	if err != nil {
		return
	}

	epochIDBuf = convert.Uint64ToBytes(data.EpochID)
	selfIndexBuf = convert.Uint64ToBytes(data.SelfIndex)
	return
}

func GetStage2TxAlphaPki(stateDb StateDB, epochID uint64, selfIndex uint64) (alphaPkis []*ecdsa.PublicKey,
	proofs []*big.Int, err error) {

	slotLeaderPrecompileAddr := GetSlotLeaderSCAddress()

	keyHash := GetSlotLeaderStage2KeyHash(convert.Uint64ToBytes(epochID), convert.Uint64ToBytes(selfIndex))

	data := stateDb.GetStateByteArray(slotLeaderPrecompileAddr, keyHash)
	if data == nil {
		log.Debug(fmt.Sprintf("try to get stateDB addr:%s, key:%s", slotLeaderPrecompileAddr.Hex(), keyHash.Hex()))
		return nil, nil, ErrNoTx2TransInDB
	}

	epID, slfIndex, _, alphaPki, proof, err := RlpUnpackStage2DataForTx(data)
	if err != nil {
		return nil, nil, err
	}

	if epID != epochID || slfIndex != selfIndex {
		return nil, nil, ErrRlpUnpackErr
	}

	return alphaPki, proof, nil
}

func GetStg1StateDbInfo(stateDb StateDB, epochID uint64, index uint64) (mi []byte, err error) {
	slotLeaderPrecompileAddr := GetSlotLeaderSCAddress()
	keyHash := GetSlotLeaderStage1KeyHash(convert.Uint64ToBytes(epochID), convert.Uint64ToBytes(index))

	// Read and Verify
	readBuf := stateDb.GetStateByteArray(slotLeaderPrecompileAddr, keyHash)
	if readBuf == nil {
		return nil, ErrNoTx1TransInDB
	}

	epID, idxID, miPoint, err := RlpUnpackStage1DataForTx(readBuf)
	if err != nil {
		return nil, ErrRlpUnpackErr
	}
	mi = crypto.FromECDSAPub(miPoint)
	//pk and mi is 65 bytes length

	if epID == epochID &&
		idxID == index &&
		err == nil {
		return
	}

	return nil, ErrVerifyStg1Data
}

func getSlotLeaderStageIndexesKeyHash(epochID []byte, slotLeaderStageIndexes string) common.Hash {
	var keyBuf bytes.Buffer
	keyBuf.Write(epochID)
	keyBuf.Write([]byte(slotLeaderStageIndexes))
	return crypto.Keccak256Hash(keyBuf.Bytes())
}

func getSlotLeaderStageKeyHash(epochID, selfIndex []byte, slotLeaderStage string) common.Hash {
	var keyBuf bytes.Buffer
	keyBuf.Write(epochID)
	keyBuf.Write(selfIndex)
	keyBuf.Write([]byte(slotLeaderStage))
	return crypto.Keccak256Hash(keyBuf.Bytes())
}

func addSlotScCallTimes(epochID uint64) error {
	buf, err := posdb.GetDb().Get(epochID, scCallTimes)
	times := uint64(0)
	if err != nil {
		if err.Error() != "leveldb: not found" {
			return err
		}
	} else {
		times = convert.BytesToUint64(buf)
	}

	times++

	posdb.GetDb().Put(epochID, scCallTimes, convert.Uint64ToBytes(times))
	return nil
}

func isInValidStage(epochID uint64, evm *EVM, kStart uint64, kEnd uint64) bool {
	eid, sid := util.CalEpochSlotID(evm.Time.Uint64())
	if epochID != eid {
		log.Warn("Tx epochID is not current epoch", "epochID", eid, "slotID", sid, "currentEpochID", epochID)

		return false
	}

	if sid > kEnd || sid < kStart {
		log.Warn("Tx is out of valid stage range", "epochID", eid, "slotID", sid, "rangeStart", kStart,
			"rangeEnd", kEnd)

		return false
	}

	return true
}

func updateSlotLeaderStageIndex(evm *EVM, epochID []byte, slotLeaderStageIndexes string, index uint64) error {
	var sendtrans [posconfig.EpochLeaderCount]bool
	var sendtransGet [posconfig.EpochLeaderCount]bool

	key := getSlotLeaderStageIndexesKeyHash(epochID, slotLeaderStageIndexes)
	bytes := evm.StateDB.GetStateByteArray(slotLeaderPrecompileAddr, key)

	if len(bytes) == 0 {
		sendtrans[index] = true
		value, err := rlp.EncodeToBytes(sendtrans)
		if err != nil {
			return err
		}
		evm.StateDB.SetStateByteArray(slotLeaderPrecompileAddr, key, value)

		log.Debug("updateSlotLeaderStageIndex", "key", key, "value", sendtrans)
	} else {
		err := rlp.DecodeBytes(bytes, &sendtransGet)
		if err != nil {
			return err
		}

		sendtransGet[index] = true
		value, err := rlp.EncodeToBytes(sendtransGet)
		if err != nil {
			return err
		}
		evm.StateDB.SetStateByteArray(slotLeaderPrecompileAddr, key, value)
		log.Debug("updateSlotLeaderStageIndex", "key", key, "value", sendtransGet)
	}
	return nil
}
