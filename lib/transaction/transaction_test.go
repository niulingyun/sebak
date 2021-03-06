package transaction

import (
	"encoding/json"
	"testing"

	"boscoin.io/sebak/lib/common"
	"boscoin.io/sebak/lib/common/keypair"
	"boscoin.io/sebak/lib/errors"
	"boscoin.io/sebak/lib/transaction/operation"

	"github.com/btcsuite/btcutil/base58"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type TestSuite struct {
	suite.Suite
	conf      common.Config
	networkID []byte
}

func (suite *TestSuite) SetupTest() {
	suite.conf = common.NewConfig()
	suite.conf.OpsLimit = 10
	suite.networkID = []byte("sebak-unittest-tx-suite")
}

func (suite *TestSuite) TestLoadTransactionSuite() {
	_, tx := TestMakeTransaction(suite.networkID, 1)

	b, err := tx.Serialize()
	require.Nil(suite.T(), err)

	var tx2 Transaction
	json.Unmarshal(b, &tx2)
	suite.T().Log(tx2)
	require.Nil(suite.T(), err)
}

func (suite *TestSuite) TestIsWellFormedTransactionSuite() {
	_, tx := TestMakeTransaction(suite.networkID, 1)

	err := tx.IsWellFormed(suite.networkID, suite.conf)
	require.Nil(suite.T(), err)
}

func (suite *TestSuite) TestIsWellFormedTransactionWithLowerFeeSuite() {
	var err error

	{ // valid fee
		kp, tx := TestMakeTransaction(suite.networkID, 3)
		tx.Sign(kp, suite.networkID)
		err = tx.IsWellFormed(suite.networkID, suite.conf)
		require.Nil(suite.T(), err)
	}

	{ // fee is over than len(Operations) * BaseFee
		kp, tx := TestMakeTransaction(suite.networkID, 3)
		tx.B.Fee = tx.B.Fee.MustAdd(1)
		tx.Sign(kp, suite.networkID)
		err = tx.IsWellFormed(suite.networkID, suite.conf)
		require.Nil(suite.T(), err)
	}

	{ // fee is lower than len(Operations) * BaseFee
		kp, tx := TestMakeTransaction(suite.networkID, 3)
		tx.B.Fee = tx.B.Fee.MustSub(1)
		tx.Sign(kp, suite.networkID)
		err = tx.IsWellFormed(suite.networkID, suite.conf)
		require.Equal(suite.T(), errors.InvalidFee, err, "Transaction shouidn't pass Fee checks")
	}

	{ // zero fee
		kp, tx := TestMakeTransaction(suite.networkID, 3)
		tx.B.Fee = common.Amount(0)
		tx.Sign(kp, suite.networkID)
		err = tx.IsWellFormed(suite.networkID, suite.conf)
		require.Equal(suite.T(), errors.InvalidFee, err, "Transaction shouidn't pass Fee checks")
	}
}

func (suite *TestSuite) TestIsWellFormedTransactionWithInvalidSourceAddressSuite() {
	var err error

	_, tx := TestMakeTransaction(suite.networkID, 1)
	tx.B.Source = "invalid-address"
	err = tx.IsWellFormed(suite.networkID, suite.conf)
	require.NotNil(suite.T(), err)
}

func (suite *TestSuite) TestIsWellFormedTransactionWithTargetAddressIsSameWithSourceAddressSuite() {
	var err error

	_, tx := TestMakeTransaction(suite.networkID, 1)
	if pop, ok := tx.B.Operations[0].B.(operation.Payable); ok {
		tx.B.Source = pop.TargetAddress()
	} else {
		require.True(suite.T(), ok)
	}
	err = tx.IsWellFormed(suite.networkID, suite.conf)
	require.NotNil(suite.T(), err, "Transaction to self should be rejected")
}

func (suite *TestSuite) TestIsWellFormedTransactionWithInvalidSignatureSuite() {
	var err error

	_, tx := TestMakeTransaction(suite.networkID, 1)
	err = tx.IsWellFormed(suite.networkID, suite.conf)
	require.Nil(suite.T(), err)

	newSignature, _ := keypair.Master("find me").Sign(append(suite.networkID, []byte(tx.B.MakeHashString())...))
	tx.H.Signature = base58.Encode(newSignature)

	err = tx.IsWellFormed(suite.networkID, suite.conf)
	require.NotNil(suite.T(), err)
}

func (suite *TestSuite) TestIsWellFormedTransactionMaxOperationsInTransactionSuite() {
	var err error

	{ // over operation.Limit
		_, tx := TestMakeTransaction(suite.networkID, suite.conf.OpsLimit+1)
		err = tx.IsWellFormed(suite.networkID, suite.conf)
		require.Equal(suite.T(), errors.TransactionHasOverMaxOperations, err)
	}

	{ // operation.Limit
		_, tx := TestMakeTransaction(suite.networkID, suite.conf.OpsLimit)
		err = tx.IsWellFormed(suite.networkID, suite.conf)
		require.Nil(suite.T(), err)
	}
}

func TestTransaction(t *testing.T) {
	suite.Run(t, new(TestSuite))
}
