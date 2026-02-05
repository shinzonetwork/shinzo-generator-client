package solana

import (
	"context"
	"fmt"

	"github.com/gagliardetto/solana-go/rpc"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
)

// Client implements chains.BlockchainClient for Solana
type Client struct {
	rpcClient  *rpc.Client
	wsClient   *rpc.Client
	network    chains.NetworkType
	commitment Commitment
}

// NewClient creates a new Solana client from chain configuration
func NewClient(config chains.ChainConfig) (*Client, error) {
	if config.RPCURL == "" {
		return nil, fmt.Errorf("Solana RPC URL is required")
	}

	rpcClient := rpc.New(config.RPCURL)

	// Determine commitment level
	commitment := CommitmentConfirmed
	if config.Commitment != "" {
		commitment = Commitment(config.Commitment)
	}

	client := &Client{
		rpcClient:  rpcClient,
		network:    config.Network,
		commitment: commitment,
	}

	// Test the connection
	ctx := context.Background()
	_, err := rpcClient.GetSlot(ctx, rpc.CommitmentConfirmed)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Solana RPC: %w", err)
	}

	logger.Sugar.Infof("Connected to Solana RPC at %s (commitment: %s)", config.RPCURL, commitment)

	return client, nil
}

// GetLatestBlockNumber implements chains.BlockchainClient - returns the latest slot
func (c *Client) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	slot, err := c.rpcClient.GetSlot(ctx, c.commitment.ToRPCCommitment())
	if err != nil {
		return 0, fmt.Errorf("failed to get latest slot: %w", err)
	}
	return slot, nil
}

// GetBlock implements chains.BlockchainClient
func (c *Client) GetBlock(ctx context.Context, slot uint64) (chains.ChainBlock, error) {
	// Configure block request options
	maxSupportedVersion := uint64(0)
	opts := &rpc.GetBlockOpts{
		Commitment:                     c.commitment.ToRPCCommitment(),
		MaxSupportedTransactionVersion: &maxSupportedVersion,
		TransactionDetails:             rpc.TransactionDetailsFull,
		Rewards:                        new(bool), // false
	}

	block, err := c.rpcClient.GetBlockWithOpts(ctx, slot, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to get block at slot %d: %w", slot, err)
	}

	return NewSolanaBlock(slot, block), nil
}

// GetBlockWithReceipts implements chains.BlockchainClient
// For Solana, transaction metadata is included in the block response
func (c *Client) GetBlockWithReceipts(ctx context.Context, slot uint64) (chains.ChainBlock, []chains.ChainReceipt, error) {
	block, err := c.GetBlock(ctx, slot)
	if err != nil {
		return nil, nil, err
	}

	solBlock, ok := block.(*SolanaBlock)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected block type: %T", block)
	}

	// Extract receipts from the block's transaction metadata
	var receipts []chains.ChainReceipt
	if solBlock.block != nil && solBlock.block.Transactions != nil {
		for _, txWithMeta := range solBlock.block.Transactions {
			tx, err := txWithMeta.GetTransaction()
			if err != nil || tx == nil {
				continue
			}

			if len(tx.Signatures) > 0 {
				receipt := NewSolanaReceipt(tx.Signatures[0].String(), txWithMeta.Meta)
				receipts = append(receipts, receipt)
			}
		}
	}

	return block, receipts, nil
}

// Close implements chains.BlockchainClient
func (c *Client) Close() error {
	// solana-go RPC client doesn't have a close method
	return nil
}

// ChainName implements chains.BlockchainClient
func (c *Client) ChainName() string {
	return string(chains.ChainTypeSolana)
}

// NetworkName implements chains.BlockchainClient
func (c *Client) NetworkName() string {
	return string(c.network)
}

// GetNetworkID implements chains.BlockchainClient
func (c *Client) GetNetworkID(ctx context.Context) (string, error) {
	// Get genesis hash as network identifier
	genesisHash, err := c.rpcClient.GetGenesisHash(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get genesis hash: %w", err)
	}
	return genesisHash.String(), nil
}

// GetInternalClient returns the underlying Solana RPC client
func (c *Client) GetInternalClient() *rpc.Client {
	return c.rpcClient
}

// GetCommitment returns the commitment level
func (c *Client) GetCommitment() Commitment {
	return c.commitment
}

// GetSlotWithCommitment gets the latest slot with a specific commitment
func (c *Client) GetSlotWithCommitment(ctx context.Context, commitment Commitment) (uint64, error) {
	slot, err := c.rpcClient.GetSlot(ctx, commitment.ToRPCCommitment())
	if err != nil {
		return 0, fmt.Errorf("failed to get slot: %w", err)
	}
	return slot, nil
}

// GetBlockHeight returns the current block height
func (c *Client) GetBlockHeight(ctx context.Context) (uint64, error) {
	height, err := c.rpcClient.GetBlockHeight(ctx, c.commitment.ToRPCCommitment())
	if err != nil {
		return 0, fmt.Errorf("failed to get block height: %w", err)
	}
	return height, nil
}

// GetFirstAvailableBlock returns the first available block slot
func (c *Client) GetFirstAvailableBlock(ctx context.Context) (uint64, error) {
	slot, err := c.rpcClient.GetFirstAvailableBlock(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get first available block: %w", err)
	}
	return slot, nil
}

// IsSlotSkipped checks if a slot was skipped (no block produced)
func (c *Client) IsSlotSkipped(ctx context.Context, slot uint64) (bool, error) {
	_, err := c.GetBlock(ctx, slot)
	if err != nil {
		// Check if the error indicates the slot was skipped
		// Solana skips slots when no block is produced
		return true, nil
	}
	return false, nil
}

// GetTransaction fetches a specific transaction by signature
func (c *Client) GetTransaction(ctx context.Context, signature string) (*SolanaTransaction, error) {
	// Note: This is a simplified implementation
	// In practice, you'd need to parse the signature and fetch the transaction
	return nil, fmt.Errorf("GetTransaction not implemented - use GetBlock for full block transactions")
}

// init registers the Solana client factory
func init() {
	chains.RegisterClientFactory(chains.ChainTypeSolana, func(config chains.ChainConfig) (chains.BlockchainClient, error) {
		return NewClient(config)
	})
}
