package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pkg/errors"

	"github.com/wavesplatform/gowaves/pkg/crypto"
	"github.com/wavesplatform/gowaves/pkg/miner/scheduler"
	"github.com/wavesplatform/gowaves/pkg/node/messages"
	"github.com/wavesplatform/gowaves/pkg/node/peers"
	"github.com/wavesplatform/gowaves/pkg/proto"
	"github.com/wavesplatform/gowaves/pkg/services"
	"github.com/wavesplatform/gowaves/pkg/state"
	"github.com/wavesplatform/gowaves/pkg/types"
)

type account struct {
	Address   proto.WavesAddress `json:"address"`
	PublicKey crypto.PublicKey   `json:"public_key"`
}

type SchedulerEmits interface {
	Emits() []scheduler.Emit
}

// default app settings
const (
	defaultBlockRequestLimit = 100
	defaultAssetDetailsLimit = 100
)

type appSettings struct {
	BlockRequestLimit uint64
	AssetDetailsLimit int
}

func defaultAppSettings() *appSettings {
	return &appSettings{
		BlockRequestLimit: defaultBlockRequestLimit,
		AssetDetailsLimit: defaultAssetDetailsLimit,
	}
}

type App struct {
	hashedApiKey  crypto.Digest
	apiKeyEnabled bool
	scheduler     SchedulerEmits
	utx           types.UtxPool
	state         state.State
	peers         peers.PeerManager
	sync          types.StateSync
	services      services.Services
	settings      *appSettings
}

func NewApp(apiKey string, scheduler SchedulerEmits, services services.Services) (*App, error) {
	return newApp(apiKey, scheduler, services, nil)
}

func newApp(apiKey string, scheduler SchedulerEmits, services services.Services, settings *appSettings) (*App, error) {
	if settings == nil {
		settings = defaultAppSettings()
	}
	digest, err := crypto.SecureHash([]byte(apiKey))
	if err != nil {
		return nil, err
	}

	return &App{
		hashedApiKey:  digest,
		apiKeyEnabled: len(apiKey) > 0,
		state:         services.State,
		scheduler:     scheduler,
		utx:           services.UtxPool,
		peers:         services.Peers,
		services:      services,
		settings:      settings,
	}, nil
}

func (a *App) TransactionsBroadcast(ctx context.Context, b []byte) (proto.Transaction, error) {
	tt := proto.TransactionTypeVersion{}
	err := json.Unmarshal(b, &tt)
	if err != nil {
		return nil, wrapToBadRequestError(err)
	}

	realType, err := proto.GuessTransactionType(&tt)
	if err != nil {
		return nil, wrapToBadRequestError(err)
	}

	err = proto.UnmarshalTransactionFromJSON(b, a.services.Scheme, realType)
	if err != nil {
		return nil, wrapToBadRequestError(err)
	}

	respCh := make(chan error, 1)

	select {
	case a.services.InternalChannel <- messages.NewBroadcastTransaction(respCh, realType):
	case <-ctx.Done():
		return nil, errors.Wrap(ctx.Err(), "failed to send internal")
	}
	var (
		delay = time.NewTimer(5 * time.Second)
		fired bool
	)
	defer func() {
		if !delay.Stop() && !fired {
			select {
			case <-delay.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, errors.Wrap(ctx.Err(), "ctx cancelled from client")
	case <-delay.C:
		fired = true
		return nil, errors.New("timeout waiting response from internal")
	case err := <-respCh:
		if err != nil {
			return nil, err
		}
		return realType, nil
	}
}

func (a *App) LoadKeys(apiKey string, password []byte) error {
	err := a.checkAuth(apiKey)
	if err != nil {
		return err
	}
	return a.services.Wallet.Load(password)
}

func (a *App) Accounts() ([]account, error) {
	seeds := a.services.Wallet.AccountSeeds()

	accounts := make([]account, 0, len(seeds))
	for _, seed := range seeds {
		_, pk, err := crypto.GenerateKeyPair(seed)
		if err != nil {
			return nil, errors.Wrap(err, "failed to generate key pair for seed")
		}
		addr, err := proto.NewAddressFromPublicKey(a.services.Scheme, pk)
		if err != nil {
			return nil, errors.Wrap(err, "failed to generate new address from public key")
		}
		accounts = append(accounts, account{Address: addr, PublicKey: pk})
	}
	return accounts, nil
}

func (a *App) checkAuth(key string) error {
	if !a.apiKeyEnabled {
		// TODO(nickeskov): use new types of errors
		return wrapToAuthError(errors.New("api key disabled"))
	}
	d, err := crypto.SecureHash([]byte(key))
	if err != nil {
		return errors.Wrap(err, "failed to calculate secure hash for API key")
	}
	if d != a.hashedApiKey {
		// TODO(nickeskov): use new types of errors
		return wrapToAuthError(errors.New("invalid api key"))
	}
	return nil
}
