// EXAMPLE: How to integrate migration into your existing indexer.go
//
// This file shows the key changes needed in pkg/indexer/indexer.go
// to add historical migration support.
//
// The main change is adding a migration call AFTER DefraDB starts
// but BEFORE live indexing begins.

package indexer

/*
=============================================================================
STEP 1: Add import for migration package
=============================================================================

Add to your imports:

import (
    // ... existing imports ...
    "github.com/shinzonetwork/shinzo-indexer-client/pkg/migration"
)


=============================================================================
STEP 2: Modify StartIndexing function
=============================================================================

In the StartIndexing function, add the migration call after DefraDB is started
and the blockHandler is created, but before the main indexing loop.

Find this section in your StartIndexing function:

    var blockHandler *defra.BlockHandler
    var blockHandlerErr error
    if !defraStarted && i.defraNode != nil {
        blockHandler, blockHandlerErr = defra.NewBlockHandlerWithNode(i.defraNode, cfg.Indexer.MaxDocsPerTxn)
        // ...
    }

And add the migration call right after it:

*/

// ExampleStartIndexingWithMigration shows the key modification needed
func (i *ChainIndexer) ExampleStartIndexingWithMigration(defraStarted bool) error {
	ctx := context.Background()
	cfg := i.cfg

	// ... (existing DefraDB startup code) ...

	// After blockHandler is created, add migration support:
	// =====================================================
	// MIGRATION: Run historical data import before live indexing
	// =====================================================
	if cfg.Migration.Enabled && i.defraNode != nil {
		logger.Sugar.Info("Migration enabled, running historical data import...")
		
		migCfg := &MigrationConfig{
			Enabled:        cfg.Migration.Enabled,
			StartBlock:     cfg.Migration.StartBlock,
			EndBlock:       cfg.Migration.EndBlock,
			BatchSize:      cfg.Migration.BatchSize,
			Workers:        cfg.Migration.Workers,
			OutputDir:      cfg.Migration.OutputDir,
			Provider:       cfg.Migration.Provider,
			Validate:       cfg.Migration.Validate,
			ValidateSample: cfg.Migration.ValidateSample,
		}
		
		if err := i.RunMigration(ctx, migCfg); err != nil {
			// Decide how to handle migration errors:
			// Option 1: Log and continue with live indexing
			logger.Sugar.Errorf("Migration failed: %v - continuing with live indexing", err)
			
			// Option 2: Fail startup (uncomment to enable)
			// return fmt.Errorf("migration failed: %w", err)
		}
		
		// Update start height if migration succeeded
		// This prevents re-indexing blocks that were just migrated
		if cfg.Migration.EndBlock > 0 {
			cfg.Indexer.StartHeight = int(cfg.Migration.EndBlock + 1)
			logger.Sugar.Infof("Updated start height to %d after migration", cfg.Indexer.StartHeight)
		}
	}
	// =====================================================
	// END MIGRATION
	// =====================================================

	// ... (rest of existing indexing code) ...

	return nil
}

/*
=============================================================================
STEP 3: Alternative - Run migration in background
=============================================================================

If you want migration to run in the background while live indexing continues,
use RunMigrationAsync instead:

*/

func (i *ChainIndexer) ExampleBackgroundMigration(defraStarted bool) error {
	ctx := context.Background()
	cfg := i.cfg

	// ... (existing DefraDB startup code) ...

	// Start background migration (non-blocking)
	if cfg.Migration.Enabled && i.defraNode != nil {
		migCfg := &MigrationConfig{
			StartBlock: cfg.Migration.StartBlock,
			EndBlock:   cfg.Migration.EndBlock,
			BatchSize:  cfg.Migration.BatchSize,
			Workers:    2, // Use fewer workers to not compete with live indexing
		}
		
		resultChan := i.RunMigrationAsync(ctx, migCfg)
		
		// Handle result in background
		go func() {
			result := <-resultChan
			if result.Err != nil {
				logger.Sugar.Errorf("Background migration failed: %v", result.Err)
			} else {
				logger.Sugar.Info("Background migration completed successfully")
			}
		}()
	}

	// Continue with live indexing immediately...
	return nil
}

/*
=============================================================================
COMPLETE EXAMPLE: Full StartIndexing with migration
=============================================================================

Here's a complete example showing where to insert the migration code
in your existing StartIndexing function:

*/

func (i *ChainIndexer) StartIndexingWithMigrationFull(defraStarted bool) error {
	ctx := context.Background()
	cfg := i.cfg

	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}

	if logger.Sugar == nil {
		logger.Init(cfg.Logger.Development)
	}

	// Start DefraDB (existing code)
	if !defraStarted {
		appCfg := toAppConfig(cfg)
		defraNode, _, err := appsdk.StartDefraInstance(appCfg,
			appsdk.NewSchemaApplierFromProvidedSchema(schema.GetSchemaForBuild()),
			constants.AllCollections...)
		if err != nil {
			return fmt.Errorf("failed to start DefraDB: %v", err)
		}
		i.defraNode = defraNode
		// ... rest of DefraDB setup ...
	}

	// Create block handler (existing code)
	var blockHandler *defra.BlockHandler
	if !defraStarted && i.defraNode != nil {
		blockHandler, _ = defra.NewBlockHandlerWithNode(i.defraNode, cfg.Indexer.MaxDocsPerTxn)
	} else {
		blockHandler, _ = defra.NewBlockHandler(cfg.DefraDB.Url)
	}

	// =====================================================
	// >>> INSERT MIGRATION HERE <<<
	// =====================================================
	if cfg.Migration.Enabled && i.defraNode != nil {
		logger.Sugar.Info("Running historical migration before live indexing...")
		
		migCfg := &MigrationConfig{
			StartBlock: cfg.Migration.StartBlock,
			EndBlock:   cfg.Migration.EndBlock,
			BatchSize:  cfg.Migration.BatchSize,
			Workers:    cfg.Migration.Workers,
			OutputDir:  cfg.Migration.OutputDir,
			Provider:   cfg.Migration.Provider,
		}
		
		if err := i.RunMigration(ctx, migCfg); err != nil {
			logger.Sugar.Errorf("Migration failed: %v", err)
			// Continue anyway - live indexing will fill gaps
		}
	}
	// =====================================================

	// Check for existing blocks (existing code)
	startHeight := int64(cfg.Indexer.StartHeight)
	nBlock, err := blockHandler.GetHighestBlockNumber(ctx)
	if err == nil && nBlock > 0 && nBlock > startHeight {
		cfg.Indexer.StartHeight = int(nBlock + 1)
		logger.Sugar.Infof("Found existing blocks up to %d, starting from %d", nBlock, cfg.Indexer.StartHeight)
	}

	// ... rest of existing indexing code ...

	return nil
}
