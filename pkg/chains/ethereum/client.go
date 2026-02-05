package ethereum

import (
	"context"
	"fmt"
	"math/big"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/rpc"
)

// Client implements chains.BlockchainClient for Ethereum
type Client struct {
	ethClient *rpc.EthereumClient
	network   chains.NetworkType
}

// NewClient creates a new Ethereum client from chain configuration
func NewClient(config chains.ChainConfig) (*Client, error) {
	ethClient, err := rpc.NewEthereumClient(config.RPCURL, config.WSURL, config.APIKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create Ethereum client: %w", err)
	}

	return &Client{
		ethClient: ethClient,
		network:   config.Network,
	}, nil
}

// NewClientFromRPC creates a new Ethereum client from an existing RPC client
func NewClientFromRPC(ethClient *rpc.EthereumClient, network chains.NetworkType) *Client {
	return &Client{
		ethClient: ethClient,
		network:   network,
	}
}

// GetLatestBlockNumber implements chains.BlockchainClient
func (c *Client) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	blockNum, err := c.ethClient.GetLatestBlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	return blockNum.Uint64(), nil
}

// GetBlock implements chains.BlockchainClient
func (c *Client) GetBlock(ctx context.Context, blockNumber uint64) (chains.ChainBlock, error) {
	block, err := c.ethClient.GetBlockByNumber(ctx, big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, err
	}
	return NewEthereumBlock(block), nil
}

// GetBlockWithReceipts implements chains.BlockchainClient
func (c *Client) GetBlockWithReceipts(ctx context.Context, blockNumber uint64) (chains.ChainBlock, []chains.ChainReceipt, error) {
	block, err := c.ethClient.GetBlockByNumber(ctx, big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, nil, err
	}

	receipts, err := c.ethClient.GetBlockReceipts(ctx, big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, nil, err
	}

	chainReceipts := make([]chains.ChainReceipt, len(receipts))
	for i, receipt := range receipts {
		chainReceipts[i] = NewEthereumReceipt(receipt)
	}

	return NewEthereumBlockWithReceipts(block, receipts), chainReceipts, nil
}

// Close implements chains.BlockchainClient
func (c *Client) Close() error {
	return c.ethClient.Close()
}

// ChainName implements chains.BlockchainClient
func (c *Client) ChainName() string {
	return string(chains.ChainTypeEthereum)
}

// NetworkName implements chains.BlockchainClient
func (c *Client) NetworkName() string {
	return string(c.network)
}

// GetNetworkID implements chains.BlockchainClient
func (c *Client) GetNetworkID(ctx context.Context) (string, error) {
	networkID, err := c.ethClient.GetNetworkID(ctx)
	if err != nil {
		return "", err
	}
	return networkID.String(), nil
}

// GetInternalClient returns the underlying Ethereum RPC client
func (c *Client) GetInternalClient() *rpc.EthereumClient {
	return c.ethClient
}

// GetTransactionReceipt fetches a single transaction receipt
func (c *Client) GetTransactionReceipt(ctx context.Context, txHash string) (*EthereumReceipt, error) {
	receipt, err := c.ethClient.GetTransactionReceipt(ctx, txHash)
	if err != nil {
		return nil, err
	}
	return NewEthereumReceipt(receipt), nil
}

// GetBlockReceipts fetches all receipts for a block
func (c *Client) GetBlockReceipts(ctx context.Context, blockNumber uint64) ([]*EthereumReceipt, error) {
	receipts, err := c.ethClient.GetBlockReceipts(ctx, big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, err
	}

	ethReceipts := make([]*EthereumReceipt, len(receipts))
	for i, receipt := range receipts {
		ethReceipts[i] = NewEthereumReceipt(receipt)
	}
	return ethReceipts, nil
}

// init registers the Ethereum client factory
func init() {
	chains.RegisterClientFactory(chains.ChainTypeEthereum, func(config chains.ChainConfig) (chains.BlockchainClient, error) {
		return NewClient(config)
	})
}
