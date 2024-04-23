package txmgr

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/Layr-Labs/eigenda/api/grpc/disperser"
	opservice "github.com/ethereum-optimism/optimism/op-service"
	opcrypto "github.com/ethereum-optimism/optimism/op-service/crypto"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	opsigner "github.com/ethereum-optimism/optimism/op-service/signer"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/urfave/cli/v2"
)

const (
	// Duplicated L1 RPC flag
	L1RPCFlagName = "l1-eth-rpc"
	// Key Management Flags (also have signer client flags)
	MnemonicFlagName   = "mnemonic"
	HDPathFlagName     = "hd-path"
	PrivateKeyFlagName = "private-key"
	// TxMgr Flags (new + legacy + some shared flags)
	NumConfirmationsFlagName            = "num-confirmations"
	SafeAbortNonceTooLowCountFlagName   = "safe-abort-nonce-too-low-count"
	FeeLimitMultiplierFlagName          = "fee-limit-multiplier"
	FeeLimitThresholdFlagName           = "txmgr.fee-limit-threshold"
	MinBaseFeeFlagName                  = "txmgr.min-basefee"
	MinTipCapFlagName                   = "txmgr.min-tip-cap"
	ResubmissionTimeoutFlagName         = "resubmission-timeout"
	NetworkTimeoutFlagName              = "network-timeout"
	TxSendTimeoutFlagName               = "txmgr.send-timeout"
	TxNotInMempoolTimeoutFlagName       = "txmgr.not-in-mempool-timeout"
	ReceiptQueryIntervalFlagName        = "txmgr.receipt-query-interval"
	DARpcFlagName                       = "da-rpc"
	DAPrimaryQuorumIDFlagName           = "da-primary-quorum-id"
	DAPrimaryAdversaryThresholdFlagName = "da-primary-adversary-threshold"
	DAPrimaryQuorumThresholdFlagName    = "da-primary-quorum-threshold"
	DAStatusQueryRetryIntervalFlagName  = "da-status-query-retry-interval"
	DAStatusQueryTimeoutFlagName        = "da-status-query-timeout"
)

var (
	SequencerHDPathFlag = &cli.StringFlag{
		Name: "sequencer-hd-path",
		Usage: "DEPRECATED: The HD path used to derive the sequencer wallet from the " +
			"mnemonic. The mnemonic flag must also be set.",
		EnvVars: []string{"OP_BATCHER_SEQUENCER_HD_PATH"},
	}
	L2OutputHDPathFlag = &cli.StringFlag{
		Name: "l2-output-hd-path",
		Usage: "DEPRECATED:The HD path used to derive the l2output wallet from the " +
			"mnemonic. The mnemonic flag must also be set.",
		EnvVars: []string{"OP_PROPOSER_L2_OUTPUT_HD_PATH"},
	}
)

type DefaultFlagValues struct {
	NumConfirmations          uint64
	SafeAbortNonceTooLowCount uint64
	FeeLimitMultiplier        uint64
	FeeLimitThresholdGwei     float64
	MinTipCapGwei             float64
	MinBaseFeeGwei            float64
	ResubmissionTimeout       time.Duration
	NetworkTimeout            time.Duration
	TxSendTimeout             time.Duration
	TxNotInMempoolTimeout     time.Duration
	ReceiptQueryInterval      time.Duration
}

var (
	DefaultBatcherFlagValues = DefaultFlagValues{
		NumConfirmations:          uint64(10),
		SafeAbortNonceTooLowCount: uint64(3),
		FeeLimitMultiplier:        uint64(5),
		FeeLimitThresholdGwei:     100.0,
		MinTipCapGwei:             1.0,
		MinBaseFeeGwei:            1.0,
		ResubmissionTimeout:       48 * time.Second,
		NetworkTimeout:            10 * time.Second,
		TxSendTimeout:             0 * time.Second,
		TxNotInMempoolTimeout:     2 * time.Minute,
		ReceiptQueryInterval:      12 * time.Second,
	}
	DefaultChallengerFlagValues = DefaultFlagValues{
		NumConfirmations:          uint64(3),
		SafeAbortNonceTooLowCount: uint64(3),
		FeeLimitMultiplier:        uint64(5),
		FeeLimitThresholdGwei:     100.0,
		MinTipCapGwei:             1.0,
		MinBaseFeeGwei:            1.0,
		ResubmissionTimeout:       24 * time.Second,
		NetworkTimeout:            10 * time.Second,
		TxSendTimeout:             2 * time.Minute,
		TxNotInMempoolTimeout:     1 * time.Minute,
		ReceiptQueryInterval:      12 * time.Second,
	}
)

func CLIFlags(envPrefix string) []cli.Flag {
	return CLIFlagsWithDefaults(envPrefix, DefaultBatcherFlagValues)
}

func CLIFlagsWithDefaults(envPrefix string, defaults DefaultFlagValues) []cli.Flag {
	prefixEnvVars := func(name string) []string {
		return opservice.PrefixEnvVar(envPrefix, name)
	}
	return append([]cli.Flag{
		&cli.StringFlag{
			Name:    MnemonicFlagName,
			Usage:   "The mnemonic used to derive the wallets for either the service",
			EnvVars: prefixEnvVars("MNEMONIC"),
		},
		&cli.StringFlag{
			Name:    HDPathFlagName,
			Usage:   "The HD path used to derive the sequencer wallet from the mnemonic. The mnemonic flag must also be set.",
			EnvVars: prefixEnvVars("HD_PATH"),
		},
		&cli.StringFlag{
			Name:    PrivateKeyFlagName,
			Usage:   "The private key to use with the service. Must not be used with mnemonic.",
			EnvVars: prefixEnvVars("PRIVATE_KEY"),
		},
		&cli.Uint64Flag{
			Name:    NumConfirmationsFlagName,
			Usage:   "Number of confirmations which we will wait after sending a transaction",
			Value:   defaults.NumConfirmations,
			EnvVars: prefixEnvVars("NUM_CONFIRMATIONS"),
		},
		&cli.Uint64Flag{
			Name:    SafeAbortNonceTooLowCountFlagName,
			Usage:   "Number of ErrNonceTooLow observations required to give up on a tx at a particular nonce without receiving confirmation",
			Value:   defaults.SafeAbortNonceTooLowCount,
			EnvVars: prefixEnvVars("SAFE_ABORT_NONCE_TOO_LOW_COUNT"),
		},
		&cli.Uint64Flag{
			Name:    FeeLimitMultiplierFlagName,
			Usage:   "The multiplier applied to fee suggestions to put a hard limit on fee increases",
			Value:   defaults.FeeLimitMultiplier,
			EnvVars: prefixEnvVars("TXMGR_FEE_LIMIT_MULTIPLIER"),
		},
		&cli.Float64Flag{
			Name:    FeeLimitThresholdFlagName,
			Usage:   "The minimum threshold (in GWei) at which fee bumping starts to be capped. Allows arbitrary fee bumps below this threshold.",
			Value:   defaults.FeeLimitThresholdGwei,
			EnvVars: prefixEnvVars("TXMGR_FEE_LIMIT_THRESHOLD"),
		},
		&cli.Float64Flag{
			Name:    MinTipCapFlagName,
			Usage:   "Enforces a minimum tip cap (in GWei) to use when determining tx fees. 1 GWei by default.",
			Value:   defaults.MinTipCapGwei,
			EnvVars: prefixEnvVars("TXMGR_MIN_TIP_CAP"),
		},
		&cli.Float64Flag{
			Name:    MinBaseFeeFlagName,
			Usage:   "Enforces a minimum base fee (in GWei) to assume when determining tx fees. 1 GWei by default.",
			Value:   defaults.MinBaseFeeGwei,
			EnvVars: prefixEnvVars("TXMGR_MIN_BASEFEE"),
		},
		&cli.DurationFlag{
			Name:    ResubmissionTimeoutFlagName,
			Usage:   "Duration we will wait before resubmitting a transaction to L1",
			Value:   defaults.ResubmissionTimeout,
			EnvVars: prefixEnvVars("RESUBMISSION_TIMEOUT"),
		},
		&cli.DurationFlag{
			Name:    NetworkTimeoutFlagName,
			Usage:   "Timeout for all network operations",
			Value:   defaults.NetworkTimeout,
			EnvVars: prefixEnvVars("NETWORK_TIMEOUT"),
		},
		&cli.DurationFlag{
			Name:    TxSendTimeoutFlagName,
			Usage:   "Timeout for sending transactions. If 0 it is disabled.",
			Value:   defaults.TxSendTimeout,
			EnvVars: prefixEnvVars("TXMGR_TX_SEND_TIMEOUT"),
		},
		&cli.DurationFlag{
			Name:    TxNotInMempoolTimeoutFlagName,
			Usage:   "Timeout for aborting a tx send if the tx does not make it to the mempool.",
			Value:   defaults.TxNotInMempoolTimeout,
			EnvVars: prefixEnvVars("TXMGR_TX_NOT_IN_MEMPOOL_TIMEOUT"),
		},
		&cli.DurationFlag{
			Name:    ReceiptQueryIntervalFlagName,
			Usage:   "Frequency to poll for receipts",
			Value:   defaults.ReceiptQueryInterval,
			EnvVars: prefixEnvVars("TXMGR_RECEIPT_QUERY_INTERVAL"),
		},
		&cli.StringFlag{
			Name:    DARpcFlagName,
			Usage:   "RPC endpoint of the EigenDA disperser",
			EnvVars: prefixEnvVars("DA_RPC"),
		},
		&cli.Uint64Flag{
			Name:    DAPrimaryAdversaryThresholdFlagName,
			Usage:   "Adversary threshold for the primary quorum of the DA layer",
			EnvVars: prefixEnvVars("DA_PRIMARY_ADVERSARY_THRESHOLD"),
		},
		&cli.Uint64Flag{
			Name:    DAPrimaryQuorumThresholdFlagName,
			Usage:   "Quorum threshold for the primary quorum of the DA layer",
			EnvVars: prefixEnvVars("DA_PRIMARY_QUORUM_THRESHOLD"),
		},
		&cli.Uint64Flag{
			Name:    DAPrimaryQuorumIDFlagName,
			Usage:   "Secondary Quorum ID of the DA layer",
			EnvVars: prefixEnvVars("DA_PRIMARY_QUORUM_ID"),
		},
		&cli.DurationFlag{
			Name:    DAStatusQueryTimeoutFlagName,
			Usage:   "Timeout for aborting an EigenDA blob dispersal if the disperser does not report that the blob has been confirmed dispersed.",
			Value:   1 * time.Minute,
			EnvVars: prefixEnvVars("DA_STATUS_QUERY_TIMEOUT"),
		},
		&cli.DurationFlag{
			Name:    DAStatusQueryRetryIntervalFlagName,
			Usage:   "Wait time between retries of EigenDA blob status queries (made while waiting for a blob to be confirmed by)",
			Value:   5 * time.Second,
			EnvVars: prefixEnvVars("DA_STATUS_QUERY_INTERVAL"),
		},
	}, opsigner.CLIFlags(envPrefix)...)
}

type CLIConfig struct {
	L1RPCURL                    string
	Mnemonic                    string
	HDPath                      string
	SequencerHDPath             string
	L2OutputHDPath              string
	PrivateKey                  string
	SignerCLIConfig             opsigner.CLIConfig
	NumConfirmations            uint64
	SafeAbortNonceTooLowCount   uint64
	FeeLimitMultiplier          uint64
	FeeLimitThresholdGwei       float64
	MinBaseFeeGwei              float64
	MinTipCapGwei               float64
	ResubmissionTimeout         time.Duration
	ReceiptQueryInterval        time.Duration
	NetworkTimeout              time.Duration
	TxSendTimeout               time.Duration
	TxNotInMempoolTimeout       time.Duration
	DARpc                       string
	DAPrimaryQuorumID           uint32
	DAPrimaryAdversaryThreshold uint32
	DAPrimaryQuorumThreshold    uint32
	DAStatusQueryRetryInterval  time.Duration
	DAStatusQueryTimeout        time.Duration
}

func NewCLIConfig(l1RPCURL string, defaults DefaultFlagValues) CLIConfig {
	return CLIConfig{
		L1RPCURL:                  l1RPCURL,
		NumConfirmations:          defaults.NumConfirmations,
		SafeAbortNonceTooLowCount: defaults.SafeAbortNonceTooLowCount,
		FeeLimitMultiplier:        defaults.FeeLimitMultiplier,
		FeeLimitThresholdGwei:     defaults.FeeLimitThresholdGwei,
		MinTipCapGwei:             defaults.MinTipCapGwei,
		MinBaseFeeGwei:            defaults.MinBaseFeeGwei,
		ResubmissionTimeout:       defaults.ResubmissionTimeout,
		NetworkTimeout:            defaults.NetworkTimeout,
		TxSendTimeout:             defaults.TxSendTimeout,
		TxNotInMempoolTimeout:     defaults.TxNotInMempoolTimeout,
		ReceiptQueryInterval:      defaults.ReceiptQueryInterval,
		SignerCLIConfig:           opsigner.NewCLIConfig(),
	}
}

func (m CLIConfig) Check() error {
	if m.L1RPCURL == "" {
		return errors.New("must provide a L1 RPC url")
	}
	if m.NumConfirmations == 0 {
		return errors.New("NumConfirmations must not be 0")
	}
	if m.NetworkTimeout == 0 {
		return errors.New("must provide NetworkTimeout")
	}
	if m.FeeLimitMultiplier == 0 {
		return errors.New("must provide FeeLimitMultiplier")
	}
	if m.MinBaseFeeGwei < m.MinTipCapGwei {
		return fmt.Errorf("minBaseFee smaller than minTipCap, have %f < %f",
			m.MinBaseFeeGwei, m.MinTipCapGwei)
	}
	if m.ResubmissionTimeout == 0 {
		return errors.New("must provide ResubmissionTimeout")
	}
	if m.ReceiptQueryInterval == 0 {
		return errors.New("must provide ReceiptQueryInterval")
	}
	if m.TxNotInMempoolTimeout == 0 {
		return errors.New("must provide TxNotInMempoolTimeout")
	}
	if m.SafeAbortNonceTooLowCount == 0 {
		return errors.New("SafeAbortNonceTooLowCount must not be 0")
	}
	if m.DARpc == "" {
		return errors.New("must provide a DA RPC url")
	}
	if m.DAPrimaryAdversaryThreshold == 0 || m.DAPrimaryAdversaryThreshold >= 100 {
		return errors.New("must provide a valid primary DA adversary threshold between (0 and 100)")
	}
	if m.DAPrimaryQuorumThreshold == 0 || m.DAPrimaryQuorumThreshold >= 100 {
		return errors.New("must provide a valid primary DA quorum threshold between (0 and 100)")
	}
	if m.DAStatusQueryTimeout == 0 {
		return errors.New("DA status query timeout must be greater than 0")
	}
	if m.DAStatusQueryRetryInterval == 0 {
		return errors.New("DA status query retry interval must be greater than 0")
	}
	if err := m.SignerCLIConfig.Check(); err != nil {
		return err
	}
	return nil
}

func ReadCLIConfig(ctx *cli.Context) CLIConfig {
	return CLIConfig{
		L1RPCURL:                    ctx.String(L1RPCFlagName),
		Mnemonic:                    ctx.String(MnemonicFlagName),
		HDPath:                      ctx.String(HDPathFlagName),
		SequencerHDPath:             ctx.String(SequencerHDPathFlag.Name),
		L2OutputHDPath:              ctx.String(L2OutputHDPathFlag.Name),
		PrivateKey:                  ctx.String(PrivateKeyFlagName),
		SignerCLIConfig:             opsigner.ReadCLIConfig(ctx),
		NumConfirmations:            ctx.Uint64(NumConfirmationsFlagName),
		SafeAbortNonceTooLowCount:   ctx.Uint64(SafeAbortNonceTooLowCountFlagName),
		FeeLimitMultiplier:          ctx.Uint64(FeeLimitMultiplierFlagName),
		FeeLimitThresholdGwei:       ctx.Float64(FeeLimitThresholdFlagName),
		MinBaseFeeGwei:              ctx.Float64(MinBaseFeeFlagName),
		MinTipCapGwei:               ctx.Float64(MinTipCapFlagName),
		ResubmissionTimeout:         ctx.Duration(ResubmissionTimeoutFlagName),
		ReceiptQueryInterval:        ctx.Duration(ReceiptQueryIntervalFlagName),
		NetworkTimeout:              ctx.Duration(NetworkTimeoutFlagName),
		TxSendTimeout:               ctx.Duration(TxSendTimeoutFlagName),
		TxNotInMempoolTimeout:       ctx.Duration(TxNotInMempoolTimeoutFlagName),
		DARpc:                       ctx.String(DARpcFlagName),
		DAPrimaryQuorumID:           Uint32(ctx, DAPrimaryQuorumIDFlagName),
		DAPrimaryAdversaryThreshold: Uint32(ctx, DAPrimaryAdversaryThresholdFlagName),
		DAPrimaryQuorumThreshold:    Uint32(ctx, DAPrimaryQuorumThresholdFlagName),
		DAStatusQueryRetryInterval:  ctx.Duration(DAStatusQueryRetryIntervalFlagName),
		DAStatusQueryTimeout:        ctx.Duration(DAStatusQueryTimeoutFlagName),
	}
}

func NewConfig(cfg CLIConfig, l log.Logger) (Config, error) {
	if err := cfg.Check(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.NetworkTimeout)
	defer cancel()
	l1, err := ethclient.DialContext(ctx, cfg.L1RPCURL)
	if err != nil {
		return Config{}, fmt.Errorf("could not dial eth client: %w", err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), cfg.NetworkTimeout)
	defer cancel()
	chainID, err := l1.ChainID(ctx)
	if err != nil {
		return Config{}, fmt.Errorf("could not dial fetch L1 chain ID: %w", err)
	}

	// Allow backwards compatible ways of specifying the HD path
	hdPath := cfg.HDPath
	if hdPath == "" && cfg.SequencerHDPath != "" {
		hdPath = cfg.SequencerHDPath
	} else if hdPath == "" && cfg.L2OutputHDPath != "" {
		hdPath = cfg.L2OutputHDPath
	}

	signerFactory, from, err := opcrypto.SignerFactoryFromConfig(l, cfg.PrivateKey, cfg.Mnemonic, hdPath, cfg.SignerCLIConfig)
	if err != nil {
		return Config{}, fmt.Errorf("could not init signer: %w", err)
	}

	feeLimitThreshold, err := eth.GweiToWei(cfg.FeeLimitThresholdGwei)
	if err != nil {
		return Config{}, fmt.Errorf("invalid fee limit threshold: %w", err)
	}

	minBaseFee, err := eth.GweiToWei(cfg.MinBaseFeeGwei)
	if err != nil {
		return Config{}, fmt.Errorf("invalid min base fee: %w", err)
	}

	minTipCap, err := eth.GweiToWei(cfg.MinTipCapGwei)
	if err != nil {
		return Config{}, fmt.Errorf("invalid min tip cap: %w", err)
	}

	disperserSecurityParams := []*disperser.SecurityParams{}
	disperserSecurityParams = append(disperserSecurityParams, &disperser.SecurityParams{
		QuorumId:           cfg.DAPrimaryQuorumID,
		AdversaryThreshold: cfg.DAPrimaryAdversaryThreshold,
		QuorumThreshold:    cfg.DAPrimaryQuorumThreshold,
	})

	return Config{
		Backend:                    l1,
		ResubmissionTimeout:        cfg.ResubmissionTimeout,
		FeeLimitMultiplier:         cfg.FeeLimitMultiplier,
		FeeLimitThreshold:          feeLimitThreshold,
		MinBaseFee:                 minBaseFee,
		MinTipCap:                  minTipCap,
		ChainID:                    chainID,
		TxSendTimeout:              cfg.TxSendTimeout,
		TxNotInMempoolTimeout:      cfg.TxNotInMempoolTimeout,
		NetworkTimeout:             cfg.NetworkTimeout,
		ReceiptQueryInterval:       cfg.ReceiptQueryInterval,
		NumConfirmations:           cfg.NumConfirmations,
		SafeAbortNonceTooLowCount:  cfg.SafeAbortNonceTooLowCount,
		Signer:                     signerFactory(chainID),
		From:                       from,
		DARpc:                      cfg.DARpc,
		DADisperserSecurityParams:  disperserSecurityParams,
		DAStatusQueryTimeout:       cfg.DAStatusQueryTimeout,
		DAStatusQueryRetryInterval: cfg.DAStatusQueryRetryInterval,
	}, nil
}

// Config houses parameters for altering the behavior of a SimpleTxManager.
type Config struct {
	Backend ETHBackend
	// ResubmissionTimeout is the interval at which, if no previously
	// published transaction has been mined, the new tx with a bumped gas
	// price will be published. Only one publication at MaxGasPrice will be
	// attempted.
	ResubmissionTimeout time.Duration

	// The multiplier applied to fee suggestions to put a hard limit on fee increases.
	FeeLimitMultiplier uint64

	// Minimum threshold (in Wei) at which the FeeLimitMultiplier takes effect.
	// On low-fee networks, like test networks, this allows for arbitrary fee bumps
	// below this threshold.
	FeeLimitThreshold *big.Int

	// Minimum base fee (in Wei) to assume when determining tx fees.
	MinBaseFee *big.Int

	// Minimum tip cap (in Wei) to enforce when determining tx fees.
	MinTipCap *big.Int

	// ChainID is the chain ID of the L1 chain.
	ChainID *big.Int

	// TxSendTimeout is how long to wait for sending a transaction.
	// By default it is unbounded. If set, this is recommended to be at least 20 minutes.
	TxSendTimeout time.Duration

	// TxNotInMempoolTimeout is how long to wait before aborting a transaction send if the transaction does not
	// make it to the mempool. If the tx is in the mempool, TxSendTimeout is used instead.
	TxNotInMempoolTimeout time.Duration

	// NetworkTimeout is the allowed duration for a single network request.
	// This is intended to be used for network requests that can be replayed.
	NetworkTimeout time.Duration

	// RequireQueryInterval is the interval at which the tx manager will
	// query the backend to check for confirmations after a tx at a
	// specific gas price has been published.
	ReceiptQueryInterval time.Duration

	// NumConfirmations specifies how many blocks are need to consider a
	// transaction confirmed.
	NumConfirmations uint64

	// SafeAbortNonceTooLowCount specifies how many ErrNonceTooLow observations
	// are required to give up on a tx at a particular nonce without receiving
	// confirmation.
	SafeAbortNonceTooLowCount uint64

	// Signer is used to sign transactions when the gas price is increased.
	Signer opcrypto.SignerFn
	From   common.Address

	// Eigenlayer Config

	// TODO(eigenlayer): Update quorum ID command-line parameters to support passing
	// and arbitrary number of quorum IDs.

	// DaRpc is the HTTP provider URL for the Data Availability node.
	DARpc string

	// Quorum IDs and SecurityParams to use when dispersing and retrieving blobs
	DADisperserSecurityParams []*disperser.SecurityParams

	// The total amount of time that the batcher will spend waiting for EigenDA to confirm a blob
	DAStatusQueryTimeout time.Duration

	// The amount of time to wait between status queries of a newly dispersed blob
	DAStatusQueryRetryInterval time.Duration
}

func SafeConvertUInt64ToUInt32(val uint64) (uint32, bool) {
	if val <= math.MaxUint32 {
		return uint32(val), true
	}
	return 0, false
}

// We add this because the urfave/cli library doesn't support uint32 specifically
func Uint32(ctx *cli.Context, flagName string) uint32 {
	daQuorumIDLong := ctx.Uint64(flagName)
	daQuorumID, success := SafeConvertUInt64ToUInt32(daQuorumIDLong)
	if !success {
		panic(fmt.Errorf("%s must be in the uint32 range", flagName))
	}
	return daQuorumID
}

func (m Config) Check() error {
	if m.Backend == nil {
		return errors.New("must provide the Backend")
	}
	if m.NumConfirmations == 0 {
		return errors.New("NumConfirmations must not be 0")
	}
	if m.NetworkTimeout == 0 {
		return errors.New("must provide NetworkTimeout")
	}
	if m.FeeLimitMultiplier == 0 {
		return errors.New("must provide FeeLimitMultiplier")
	}
	if m.MinBaseFee != nil && m.MinTipCap != nil && m.MinBaseFee.Cmp(m.MinTipCap) == -1 {
		return fmt.Errorf("minBaseFee smaller than minTipCap, have %v < %v",
			m.MinBaseFee, m.MinTipCap)
	}
	if m.ResubmissionTimeout == 0 {
		return errors.New("must provide ResubmissionTimeout")
	}
	if m.ReceiptQueryInterval == 0 {
		return errors.New("must provide ReceiptQueryInterval")
	}
	if m.TxNotInMempoolTimeout == 0 {
		return errors.New("must provide TxNotInMempoolTimeout")
	}
	if m.SafeAbortNonceTooLowCount == 0 {
		return errors.New("SafeAbortNonceTooLowCount must not be 0")
	}
	if m.Signer == nil {
		return errors.New("must provide the Signer")
	}
	if m.ChainID == nil {
		return errors.New("must provide the ChainID")
	}
	return nil
}
