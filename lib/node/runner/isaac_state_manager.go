package runner

import (
	"sync"
	"time"

	"boscoin.io/sebak/lib/ballot"
	"boscoin.io/sebak/lib/block"
	"boscoin.io/sebak/lib/common"
	"boscoin.io/sebak/lib/consensus"
	"boscoin.io/sebak/lib/voting"
)

// ISAACStateManager manages the ISAACState.
// The most important function `Start()` is called in StartStateManager() function in node_runner.go by goroutine.
type ISAACStateManager struct {
	sync.RWMutex

	nr              *NodeRunner
	state           consensus.ISAACState
	stateTransit    chan consensus.ISAACState
	stop            chan struct{}
	blockTimeBuffer time.Duration              // the time to wait to adjust the block creation time.
	transitSignal   func(consensus.ISAACState) // the function is called when the ISAACState is changed.
	genesis         time.Time                  // the time at which the GenesisBlock was saved. It is used for calculating `blockTimeBuffer`.

	Conf common.Config
}

func NewISAACStateManager(nr *NodeRunner, conf common.Config) *ISAACStateManager {
	p := &ISAACStateManager{
		nr: nr,
		state: consensus.ISAACState{
			Round:       0,
			Height:      0,
			BallotState: ballot.StateINIT,
		},
		stateTransit:    make(chan consensus.ISAACState),
		stop:            make(chan struct{}),
		blockTimeBuffer: 2 * time.Second,
		transitSignal:   func(consensus.ISAACState) {},
		Conf:            conf,
	}

	genesisBlock := block.GetGenesis(nr.storage)
	p.genesis = genesisBlock.Header.Timestamp

	return p
}

func (sm *ISAACStateManager) SetBlockTimeBuffer() {
	sm.nr.Log().Debug("begin ISAACStateManager.SetBlockTimeBuffer()", "ISAACState", sm.State())
	b := sm.nr.Consensus().LatestBlock()
	ballotProposedTime := getBallotProposedTime(b.Confirmed)
	sm.blockTimeBuffer = calculateBlockTimeBuffer(
		sm.Conf.BlockTime,
		calculateAverageBlockTime(sm.genesis, b.Height),
		time.Now().Sub(ballotProposedTime),
		1*time.Second,
	)
	sm.nr.Log().Debug(
		"calculated blockTimeBuffer",
		"blockTimeBuffer", sm.blockTimeBuffer,
		"blockTime", sm.Conf.BlockTime,
		"genesis", sm.genesis,
		"height", b.Height,
		"confirmed", b.Confirmed,
		"now", time.Now(),
	)

	return
}

func getBallotProposedTime(timeStr string) time.Time {
	ballotProposedTime, _ := common.ParseISO8601(timeStr)
	return ballotProposedTime
}

func calculateAverageBlockTime(genesis time.Time, blockHeight uint64) time.Duration {
	genesisBlockHeight := uint64(1)
	height := blockHeight - genesisBlockHeight
	sinceGenesis := time.Now().Sub(genesis)

	if height == 0 {
		return sinceGenesis
	} else {
		return sinceGenesis / time.Duration(height)
	}
}

func calculateBlockTimeBuffer(goal, average, untilNow, delta time.Duration) time.Duration {
	var blockTimeBuffer time.Duration

	epsilon := 50 * time.Millisecond
	if average >= goal {
		if average-goal < epsilon {
			blockTimeBuffer = goal - untilNow
		} else {
			blockTimeBuffer = goal - delta - untilNow
		}
	} else {
		if goal-average < epsilon {
			blockTimeBuffer = goal - untilNow
		} else {
			blockTimeBuffer = goal + delta - untilNow
		}
	}
	if blockTimeBuffer < 0 {
		blockTimeBuffer = 0
	}
	return blockTimeBuffer
}

func (sm *ISAACStateManager) SetTransitSignal(f func(consensus.ISAACState)) {
	sm.Lock()
	defer sm.Unlock()
	sm.transitSignal = f
}

func (sm *ISAACStateManager) TransitISAACState(height uint64, round uint64, ballotState ballot.State) {
	sm.RLock()
	current := sm.state
	sm.RUnlock()

	target := consensus.ISAACState{
		Height:      height,
		Round:       round,
		BallotState: ballotState,
	}

	if current.IsLater(target) {
		go func() {
			sm.stateTransit <- target
		}()
	}
}

func (sm *ISAACStateManager) IncreaseRound() {
	state := sm.State()
	sm.nr.Log().Debug("begin ISAACStateManager.IncreaseRound()", "height", state.Height, "round", state.Round, "state", state.BallotState)
	sm.TransitISAACState(state.Height, state.Round+1, ballot.StateINIT)
}

func (sm *ISAACStateManager) NextHeight() {
	state := sm.State()
	sm.nr.Log().Debug("begin ISAACStateManager.NextHeight()", "height", state.Height, "round", state.Round, "state", state.BallotState)
	sm.TransitISAACState(state.Height+1, 0, ballot.StateINIT)
}

// In `Start()` method a node proposes ballot.
// Or it sets or resets timeout. If it is expired, it broadcasts B(`EXP`).
// And it manages the node round.
func (sm *ISAACStateManager) Start() {
	sm.nr.localNode.SetConsensus()
	sm.nr.Log().Debug("begin ISAACStateManager.Start()", "ISAACState", sm.State())
	go func() {
		timer := time.NewTimer(time.Duration(1 * time.Hour))
		for {
			select {
			case <-timer.C:
				sm.nr.Log().Debug("timeout", "ISAACState", sm.State())
				if sm.State().BallotState == ballot.StateACCEPT {
					sm.SetBlockTimeBuffer()
					sm.IncreaseRound()
					break
				}
				go sm.broadcastExpiredBallot(sm.State())
				sm.setBallotState(sm.State().BallotState.Next())
				sm.resetTimer(timer, sm.State().BallotState)
				sm.transitSignal(sm.State())

			case state := <-sm.stateTransit:
				switch state.BallotState {
				case ballot.StateINIT:
					sm.proposeOrWait(timer, state)
				case ballot.StateSIGN:
					sm.setState(state)
					sm.transitSignal(state)
					timer.Reset(sm.Conf.TimeoutSIGN)
				case ballot.StateACCEPT:
					sm.setState(state)
					sm.transitSignal(state)
					timer.Reset(sm.Conf.TimeoutACCEPT)
				case ballot.StateALLCONFIRM:
					sm.setState(state)
					sm.transitSignal(state)
					sm.SetBlockTimeBuffer()
					sm.NextHeight()
				}

			case <-sm.stop:
				return
			}
		}
	}()
}

func (sm *ISAACStateManager) broadcastExpiredBallot(state consensus.ISAACState) {
	sm.nr.Log().Debug("begin broadcastExpiredBallot", "ISAACState", state)
	b := sm.nr.consensus.LatestBlock()
	basis := voting.Basis{
		Round:     state.Round,
		Height:    b.Height,
		BlockHash: b.Hash,
		TotalTxs:  b.TotalTxs,
		TotalOps:  b.TotalOps,
	}

	proposerAddr := sm.nr.consensus.SelectProposer(b.Height, state.Round)

	newExpiredBallot := ballot.NewBallot(sm.nr.localNode.Address(), proposerAddr, basis, []string{})
	newExpiredBallot.SetVote(state.BallotState.Next(), voting.EXP)

	opc, _ := ballot.NewCollectTxFeeFromBallot(*newExpiredBallot, sm.nr.CommonAccountAddress)
	opi, _ := ballot.NewInflationFromBallot(*newExpiredBallot, sm.nr.CommonAccountAddress, sm.nr.InitialBalance)
	ptx, _ := ballot.NewProposerTransactionFromBallot(*newExpiredBallot, opc, opi)

	newExpiredBallot.SetProposerTransaction(ptx)
	newExpiredBallot.SignByProposer(sm.nr.localNode.Keypair(), sm.nr.networkID)
	newExpiredBallot.Sign(sm.nr.localNode.Keypair(), sm.nr.networkID)

	sm.nr.Log().Debug("broadcast", "ballot", *newExpiredBallot)
	sm.nr.ConnectionManager().Broadcast(*newExpiredBallot)
}

func (sm *ISAACStateManager) resetTimer(timer *time.Timer, state ballot.State) {
	switch state {
	case ballot.StateINIT:
		timer.Reset(sm.Conf.TimeoutINIT)
	case ballot.StateSIGN:
		timer.Reset(sm.Conf.TimeoutSIGN)
	case ballot.StateACCEPT:
		timer.Reset(sm.Conf.TimeoutACCEPT)
	}
}

// In proposeOrWait,
// if nr.localNode is proposer, it proposes new ballot,
// but if not, it waits for receiving ballot from the other proposer.
func (sm *ISAACStateManager) proposeOrWait(timer *time.Timer, state consensus.ISAACState) {
	timer.Reset(time.Duration(1 * time.Hour))
	proposer := sm.nr.Consensus().SelectProposer(state.Height, state.Round)
	log.Debug("selected proposer", "proposer", proposer)

	if proposer == sm.nr.localNode.Address() {
		time.Sleep(sm.blockTimeBuffer)
		if _, err := sm.nr.proposeNewBallot(state.Round); err == nil {
			log.Debug("propose new ballot", "proposer", proposer, "round", state.Round, "ballotState", ballot.StateSIGN)
		} else {
			log.Error("failed to proposeNewBallot", "height", sm.nr.consensus.LatestBlock().Height, "error", err)
		}
		timer.Reset(sm.Conf.TimeoutINIT)
	} else {
		timer.Reset(sm.blockTimeBuffer + sm.Conf.TimeoutINIT)
	}
	sm.setState(state)
	sm.transitSignal(state)
}

func (sm *ISAACStateManager) State() consensus.ISAACState {
	sm.RLock()
	defer sm.RUnlock()
	return sm.state
}

func (sm *ISAACStateManager) setState(state consensus.ISAACState) {
	sm.Lock()
	defer sm.Unlock()
	sm.nr.Log().Debug("begin ISAACStateManager.setState()", "state", state)
	sm.state = state

	return
}

func (sm *ISAACStateManager) setBallotState(ballotState ballot.State) {
	sm.Lock()
	defer sm.Unlock()
	sm.nr.Log().Debug("begin ISAACStateManager.setBallotState()", "ballotState", ballotState)
	sm.state.BallotState = ballotState

	return
}

func (sm *ISAACStateManager) Stop() {
	go func() {
		sm.stop <- struct{}{}
	}()
}
