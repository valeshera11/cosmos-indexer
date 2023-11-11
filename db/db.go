package db

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DefiantLabs/cosmos-indexer/config"
	"github.com/DefiantLabs/cosmos-indexer/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func GetAddresses(addressList []string, db *gorm.DB) ([]models.Address, error) {
	// Look up all DB Addresses that match the search
	var addresses []models.Address
	result := db.Where("address IN ?", addressList).Find(&addresses)
	fmt.Printf("Found %d addresses in the db\n", result.RowsAffected)
	if result.Error != nil {
		config.Log.Error("Error searching DB for addresses.", result.Error)
	}

	return addresses, result.Error
}

// PostgresDbConnect connects to the database according to the passed in parameters
func PostgresDbConnect(host string, port string, database string, user string, password string, level string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable", host, port, database, user, password)
	gormLogLevel := logger.Silent

	if level == "info" {
		gormLogLevel = logger.Info
	}
	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(gormLogLevel)})
}

// PostgresDbConnect connects to the database according to the passed in parameters
func PostgresDbConnectLogInfo(host string, port string, database string, user string, password string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable", host, port, database, user, password)
	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Info)})
}

// MigrateModels runs the gorm automigrations with all the db models. This will migrate as needed and do nothing if nothing has changed.
func MigrateModels(db *gorm.DB) error {
	if err := migrateChainModels(db); err != nil {
		return err
	}

	if err := migrateBlockModels(db); err != nil {
		return err
	}

	if err := migrateDenomModels(db); err != nil {
		return err
	}

	if err := migrateTXModels(db); err != nil {
		return err
	}

	return nil
}

func migrateChainModels(db *gorm.DB) error {
	return db.AutoMigrate(
		&models.Chain{},
	)
}

func migrateBlockModels(db *gorm.DB) error {
	return db.AutoMigrate(
		&models.Block{},
		&models.BlockEvent{},
		&models.BlockEventType{},
		&models.BlockEventAttribute{},
		&models.BlockEventAttributeKey{},
		&models.FailedBlock{},
		&models.FailedEventBlock{},
	)
}

func migrateDenomModels(db *gorm.DB) error {
	return db.AutoMigrate(
		&models.Denom{},
		&models.DenomUnit{},
	)
}

func migrateTXModels(db *gorm.DB) error {
	return db.AutoMigrate(
		&models.Tx{},
		&models.Fee{},
		&models.Address{},
		&models.MessageType{},
		&models.Message{},
		&models.FailedTx{},
		&models.FailedMessage{},
		&models.MessageEvent{},
		&models.MessageEventType{},
		&models.MessageEventAttribute{},
		&models.MessageEventAttributeKey{},
	)
}

func GetFailedBlocks(db *gorm.DB, chainID uint) []models.FailedBlock {
	var failedBlocks []models.FailedBlock
	db.Table("failed_blocks").Where("chain_id = ?::int", chainID).Order("height asc").Scan(&failedBlocks)
	return failedBlocks
}

func GetFirstMissingBlockInRange(db *gorm.DB, start, end int64, chainID uint) int64 {
	// Find the highest block we have indexed so far
	currMax := GetHighestIndexedBlock(db, chainID)

	// If this is after the start date, fine the first missing block between the desired start, and the highest we have indexed +1
	if currMax.Height > start {
		end = currMax.Height + 1
	}

	var firstMissingBlock int64
	err := db.Raw(`SELECT s.i AS missing_blocks
						FROM generate_series($1::int,$2::int) s(i)
						WHERE NOT EXISTS (SELECT 1 FROM blocks WHERE height = s.i AND chain_id = $3::int AND tx_indexed = true AND time_stamp != '0001-01-01T00:00:00.000Z')
						ORDER BY s.i ASC LIMIT 1;`, start, end, chainID).Row().Scan(&firstMissingBlock)
	if err != nil {
		if !strings.Contains(err.Error(), "no rows in result set") {
			config.Log.Fatalf("Unable to find start block. Err: %v", err)
		}
		firstMissingBlock = start
	}

	return firstMissingBlock
}

func GetDBChainID(db *gorm.DB, chain models.Chain) (uint, error) {
	if err := db.Where("chain_id = ?", chain.ChainID).FirstOrCreate(&chain).Error; err != nil {
		config.Log.Error("Error getting/creating chain DB object.", err)
		return chain.ID, err
	}
	return chain.ID, nil
}

func GetHighestIndexedBlock(db *gorm.DB, chainID uint) models.Block {
	var block models.Block
	// this can potentially be optimized by getting max first and selecting it (this gets translated into a select * limit 1)
	db.Table("blocks").Where("chain_id = ?::int AND tx_indexed = true AND time_stamp != '0001-01-01T00:00:00.000Z'", chainID).Order("height desc").First(&block)
	return block
}

func GetBlocksFromStart(db *gorm.DB, chainID uint, startHeight int64, endHeight int64) ([]models.Block, error) {
	var blocks []models.Block

	initialWhere := db.Where("chain_id = ?::int AND time_stamp != '0001-01-01T00:00:00.000Z' AND height >= ?", chainID, startHeight)

	if endHeight != -1 {
		initialWhere = initialWhere.Where("height <= ?", endHeight)
	}

	if err := initialWhere.Find(&blocks).Error; err != nil {
		return nil, err
	}

	return blocks, nil
}

func GetHighestEventIndexedBlock(db *gorm.DB, chainID uint) (models.Block, error) {
	var block models.Block
	// this can potentially be optimized by getting max first and selecting it (this gets translated into a select * limit 1)
	err := db.Table("blocks").Where("chain_id = ?::int AND block_events_indexed = true AND time_stamp != '0001-01-01T00:00:00.000Z'", chainID).Order("height desc").First(&block).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return block, nil
	}

	return block, err
}

func BlockEventsAlreadyIndexed(blockHeight int64, chainID uint, db *gorm.DB) (bool, error) {
	var exists bool
	err := db.Raw(`SELECT count(*) > 0 FROM blocks WHERE height = ?::int AND chain_id = ?::int AND block_events_indexed = true AND time_stamp != '0001-01-01T00:00:00.000Z';`, blockHeight, chainID).Row().Scan(&exists)
	return exists, err
}

func UpsertFailedBlock(db *gorm.DB, blockHeight int64, chainID string, chainName string) error {
	return db.Transaction(func(dbTransaction *gorm.DB) error {
		failedBlock := models.FailedBlock{Height: blockHeight, Chain: models.Chain{ChainID: chainID, Name: chainName}}

		if err := dbTransaction.Where(&failedBlock.Chain).FirstOrCreate(&failedBlock.Chain).Error; err != nil {
			config.Log.Error("Error creating chain DB object.", err)
			return err
		}

		if err := dbTransaction.Where(&failedBlock).FirstOrCreate(&failedBlock).Error; err != nil {
			config.Log.Error("Error creating failed block DB object.", err)
			return err
		}
		return nil
	})
}

func UpsertFailedEventBlock(db *gorm.DB, blockHeight int64, chainID string, chainName string) error {
	return db.Transaction(func(dbTransaction *gorm.DB) error {
		failedEventBlock := models.FailedEventBlock{Height: blockHeight, Chain: models.Chain{ChainID: chainID, Name: chainName}}

		if err := dbTransaction.Where(&failedEventBlock.Chain).FirstOrCreate(&failedEventBlock.Chain).Error; err != nil {
			config.Log.Error("Error creating chain DB object.", err)
			return err
		}

		if err := dbTransaction.Where(&failedEventBlock).FirstOrCreate(&failedEventBlock).Error; err != nil {
			config.Log.Error("Error creating failed event block DB object.", err)
			return err
		}
		return nil
	})
}

func IndexNewBlock(db *gorm.DB, blockHeight int64, blockTime time.Time, txs []TxDBWrapper, dbChainID uint) error {
	// consider optimizing the transaction, but how? Ordering matters due to foreign key constraints
	// Order required: Block -> (For each Tx: Signer Address -> Tx -> (For each Message: Message -> Taxable Events))
	// Also, foreign key relations are struct value based so create needs to be called first to get right foreign key ID
	return db.Transaction(func(dbTransaction *gorm.DB) error {
		// remove from failed blocks if exists
		if err := dbTransaction.
			Exec("DELETE FROM failed_blocks WHERE height = ? AND blockchain_id = ?", blockHeight, dbChainID).
			Error; err != nil {
			config.Log.Error("Error updating failed block.", err)
			return err
		}

		// create block if it doesn't exist
		blockOnly := models.Block{Height: blockHeight, TimeStamp: blockTime, TxIndexed: true, ChainID: dbChainID}
		if err := dbTransaction.
			Where(models.Block{Height: blockHeight, ChainID: dbChainID}).
			Assign(models.Block{TxIndexed: true, TimeStamp: blockTime}).
			FirstOrCreate(&blockOnly).Error; err != nil {
			config.Log.Error("Error getting/creating block DB object.", err)
			return err
		}

		// pull txes and insert them
		var uniqueTxes = make(map[string]models.Tx)
		for _, tx := range txs {
			tx.Tx.BlockID = blockOnly.ID
			uniqueTxes[tx.Tx.Hash] = tx.Tx
		}

		var txesSlice []models.Tx
		for _, tx := range uniqueTxes {
			// TODO Remove this hack, fees are broken until they are inserted first (alongside the address they are associated with)
			tx.Fees = nil
			txesSlice = append(txesSlice, tx)
		}

		if len(txesSlice) != 0 {
			if err := dbTransaction.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "hash"}},
				DoUpdates: clause.AssignmentColumns([]string{"code", "block_id", "signer_address_id"}),
			}).Create(txesSlice).Error; err != nil {
				config.Log.Error("Error getting/creating txes.", err)
				return err
			}
		}

		for _, tx := range txesSlice {
			uniqueTxes[tx.Hash] = tx
		}

		// Create unique message types and post-process them into the messages
		fullUniqueBlockMessageTypes, err := indexMessageTypes(dbTransaction, txs)

		if err != nil {
			return err
		}

		fullUniqueBlockMessageEventTypes, err := indexMessageEventTypes(dbTransaction, txs)

		if err != nil {
			return err
		}

		fullUniqueBlockMessageEventAttributeKeys, err := indexMessageEventAttributeKeys(dbTransaction, txs)

		if err != nil {
			return err
		}

		// This complex set of loops is to ensure that foreign key relations are created and attached to downstream models before batch insertion is executed.
		// We are trading off in-app performance for batch insertion here and should consider complexity increase vs performance increase.
		for _, tx := range txs {
			tx.Tx = uniqueTxes[tx.Tx.Hash]
			var messagesSlice []*models.Message
			for messageIndex := range tx.Messages {
				tx.Messages[messageIndex].Message.TxID = tx.Tx.ID
				tx.Messages[messageIndex].Message.Tx = tx.Tx
				tx.Messages[messageIndex].Message.MessageTypeID = fullUniqueBlockMessageTypes[tx.Messages[messageIndex].Message.MessageType.MessageType].ID

				tx.Messages[messageIndex].Message.MessageType = fullUniqueBlockMessageTypes[tx.Messages[messageIndex].Message.MessageType.MessageType]
				for eventIndex := range tx.Messages[messageIndex].MessageEvents {
					tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent.MessageEventTypeID = fullUniqueBlockMessageEventTypes[tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent.MessageEventType.Type].ID
					tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent.MessageEventType = fullUniqueBlockMessageEventTypes[tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent.MessageEventType.Type]

					for attributeIndex := range tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes {
						tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes[attributeIndex].MessageEventAttributeKeyID = fullUniqueBlockMessageEventAttributeKeys[tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes[attributeIndex].MessageEventAttributeKey.Key].ID
						tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes[attributeIndex].MessageEventAttributeKey = fullUniqueBlockMessageEventAttributeKeys[tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes[attributeIndex].MessageEventAttributeKey.Key]
					}
				}

				messagesSlice = append(messagesSlice, &tx.Messages[messageIndex].Message)
			}

			if len(messagesSlice) != 0 {
				if err := dbTransaction.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "tx_id"}, {Name: "message_index"}},
					DoUpdates: clause.AssignmentColumns([]string{"message_type_id"}),
				}).Create(messagesSlice).Error; err != nil {
					config.Log.Error("Error getting/creating messages.", err)
					return err
				}
			}

			var messagesEventsSlice []*models.MessageEvent
			for messageIndex := range tx.Messages {
				for eventIndex := range tx.Messages[messageIndex].MessageEvents {
					tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent.MessageID = tx.Messages[messageIndex].Message.ID
					tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent.Message = tx.Messages[messageIndex].Message

					messagesEventsSlice = append(messagesEventsSlice, &tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent)
				}
			}

			if len(messagesEventsSlice) != 0 {
				if err := dbTransaction.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "message_id"}, {Name: "index"}},
					DoUpdates: clause.AssignmentColumns([]string{"message_event_type_id"}),
				}).Create(messagesEventsSlice).Error; err != nil {
					config.Log.Error("Error getting/creating message events.", err)
					return err
				}
			}

			var messagesEventsAttributesSlice []*models.MessageEventAttribute
			for messageIndex := range tx.Messages {
				for eventIndex := range tx.Messages[messageIndex].MessageEvents {
					for attributeIndex := range tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes {
						tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes[attributeIndex].MessageEventID = tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent.ID
						tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes[attributeIndex].MessageEvent = tx.Messages[messageIndex].MessageEvents[eventIndex].MessageEvent

						messagesEventsAttributesSlice = append(messagesEventsAttributesSlice, &tx.Messages[messageIndex].MessageEvents[eventIndex].Attributes[attributeIndex])
					}
				}
			}

			if len(messagesEventsAttributesSlice) != 0 {
				if err := dbTransaction.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "message_event_id"}, {Name: "index"}},
					DoUpdates: clause.AssignmentColumns([]string{"value", "message_event_attribute_key_id"}),
				}).Create(messagesEventsAttributesSlice).Error; err != nil {
					config.Log.Error("Error getting/creating message event attributes.", err)
					return err
				}
			}
		}

		// for _, transaction := range txs {
		// 	txOnly := models.Tx{
		// 		Hash:            transaction.Tx.Hash,
		// 		Code:            transaction.Tx.Code,
		// 		BlockID:         blockOnly.ID,
		// 		SignerAddressID: nil,
		// 	}

		// 	// store the signer address if there is one
		// 	if transaction.SignerAddress.Address != "" {
		// 		// viewing gorm logs shows this gets translated into a single ON CONFLICT DO NOTHING RETURNING "id"
		// 		if err := dbTransaction.Where(models.Address{Address: transaction.SignerAddress.Address}).
		// 			FirstOrCreate(&transaction.SignerAddress).
		// 			Error; err != nil {
		// 			config.Log.Error("Error getting/creating signer address for tx.", err)
		// 			return err
		// 		}
		// 		// store created db model in signer address, creates foreign key relation
		// 		txOnly.SignerAddressID = &transaction.SignerAddress.ID
		// 	}

		// 	// store the TX
		// 	if err := dbTransaction.Where(models.Tx{Hash: txOnly.Hash}).FirstOrCreate(&txOnly).Error; err != nil {
		// 		config.Log.Error("Error creating tx.", err)
		// 		return err
		// 	}

		// 	for _, fee := range transaction.Tx.Fees {
		// 		feeOnly := models.Fee{
		// 			TxID:           txOnly.ID,
		// 			Amount:         fee.Amount,
		// 			DenominationID: fee.Denomination.ID,
		// 		}
		// 		if fee.PayerAddress.Address != "" {
		// 			if err := dbTransaction.Where(models.Address{Address: fee.PayerAddress.Address}).
		// 				FirstOrCreate(&fee.PayerAddress).
		// 				Error; err != nil {
		// 				config.Log.Error("Error getting/creating fee payer address.", err)
		// 				return err
		// 			}

		// 			// creates foreign key relation.
		// 			feeOnly.PayerAddressID = fee.PayerAddress.ID
		// 		} else if fee.PayerAddress.Address == "" {
		// 			return errors.New("fee cannot have empty payer address")
		// 		}

		// 		if fee.Denomination.Base == "" || fee.Denomination.Symbol == "" {
		// 			return fmt.Errorf("denom not cached for base %s and symbol %s", fee.Denomination.Base, fee.Denomination.Symbol)
		// 		}

		// 		// store the Fee
		// 		if err := dbTransaction.Where(models.Fee{TxID: feeOnly.TxID, DenominationID: feeOnly.DenominationID}).FirstOrCreate(&feeOnly).Error; err != nil {
		// 			config.Log.Error("Error creating fee.", err)
		// 			return err
		// 		}
		// 	}

		// 	for _, message := range transaction.Messages {
		// 		if message.Message.MessageType.MessageType == "" {
		// 			config.Log.Fatal("Message type not getting to DB")
		// 		}
		// 		if err := dbTransaction.Where(&message.Message.MessageType).FirstOrCreate(&message.Message.MessageType).Error; err != nil {
		// 			config.Log.Error("Error getting/creating message_type.", err)
		// 			return err
		// 		}

		// 		msgOnly := models.Message{
		// 			TxID:          txOnly.ID,
		// 			MessageTypeID: message.Message.MessageType.ID,
		// 			MessageIndex:  message.Message.MessageIndex,
		// 		}

		// 		// Store the msg
		// 		if err := dbTransaction.Where(models.Message{TxID: msgOnly.TxID, MessageTypeID: msgOnly.MessageTypeID, MessageIndex: msgOnly.MessageIndex}).FirstOrCreate(&msgOnly).Error; err != nil {
		// 			config.Log.Error("Error creating message.", err)
		// 			return err
		// 		}

		// 		// TODO: Store message events

		// 	}
		// }

		return nil
	})
}

func indexMessageTypes(db *gorm.DB, txs []TxDBWrapper) (map[string]models.MessageType, error) {
	var fullUniqueBlockMessageTypes = make(map[string]models.MessageType)
	for _, tx := range txs {
		for messageTypeKey, messageType := range tx.UniqueMessageTypes {
			fullUniqueBlockMessageTypes[messageTypeKey] = messageType
		}
	}

	var messageTypesSlice []models.MessageType
	for _, messageType := range fullUniqueBlockMessageTypes {
		messageTypesSlice = append(messageTypesSlice, messageType)
	}

	if len(messageTypesSlice) != 0 {
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "message_type"}},
			DoUpdates: clause.AssignmentColumns([]string{"message_type"}),
		}).Create(messageTypesSlice).Error; err != nil {
			config.Log.Error("Error getting/creating message types.", err)
			return nil, err
		}
	}

	for _, messageType := range messageTypesSlice {
		fullUniqueBlockMessageTypes[messageType.MessageType] = messageType
	}

	return fullUniqueBlockMessageTypes, nil
}

func indexMessageEventTypes(db *gorm.DB, txs []TxDBWrapper) (map[string]models.MessageEventType, error) {
	var fullUniqueBlockMessageEventTypes = make(map[string]models.MessageEventType)

	for _, tx := range txs {
		for messageEventTypeKey, messageEventType := range tx.UniqueMessageEventTypes {
			fullUniqueBlockMessageEventTypes[messageEventTypeKey] = messageEventType
		}
	}

	var messageTypesSlice []models.MessageEventType
	for _, messageType := range fullUniqueBlockMessageEventTypes {
		messageTypesSlice = append(messageTypesSlice, messageType)
	}

	if len(messageTypesSlice) != 0 {
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "type"}},
			DoUpdates: clause.AssignmentColumns([]string{"type"}),
		}).Create(messageTypesSlice).Error; err != nil {
			config.Log.Error("Error getting/creating message event types.", err)
			return nil, err
		}
	}

	for _, messageType := range messageTypesSlice {
		fullUniqueBlockMessageEventTypes[messageType.Type] = messageType
	}

	return fullUniqueBlockMessageEventTypes, nil

}

func indexMessageEventAttributeKeys(db *gorm.DB, txs []TxDBWrapper) (map[string]models.MessageEventAttributeKey, error) {
	var fullUniqueMessageEventAttributeKeys = make(map[string]models.MessageEventAttributeKey)

	for _, tx := range txs {
		for messageEventAttributeKey, messageEventAttribute := range tx.UniqueMessageAttributeKeys {
			fullUniqueMessageEventAttributeKeys[messageEventAttributeKey] = messageEventAttribute
		}
	}

	var messageEventAttributeKeysSlice []models.MessageEventAttributeKey
	for _, messageEventAttributeKey := range fullUniqueMessageEventAttributeKeys {
		messageEventAttributeKeysSlice = append(messageEventAttributeKeysSlice, messageEventAttributeKey)
	}

	if len(messageEventAttributeKeysSlice) != 0 {
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"key"}),
		}).Create(messageEventAttributeKeysSlice).Error; err != nil {
			config.Log.Error("Error getting/creating message event attribute keys.", err)
			return nil, err
		}
	}

	for _, messageEventAttributeKey := range messageEventAttributeKeysSlice {
		fullUniqueMessageEventAttributeKeys[messageEventAttributeKey.Key] = messageEventAttributeKey
	}

	return fullUniqueMessageEventAttributeKeys, nil
}

func UpsertDenoms(db *gorm.DB, denoms []DenomDBWrapper) error {
	return db.Transaction(func(dbTransaction *gorm.DB) error {
		for _, denom := range denoms {
			if err := dbTransaction.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "base"}},
				DoUpdates: clause.AssignmentColumns([]string{"symbol", "name"}),
			}).Create(&denom.Denom).Error; err != nil {
				return err
			}

			for _, denomUnit := range denom.DenomUnits {
				denomUnit.DenomUnit.Denom = denom.Denom

				if err := dbTransaction.Clauses(clause.OnConflict{
					DoNothing: true,
				}).Create(&denomUnit.DenomUnit).Error; err != nil {
					return err
				}

			}
		}
		return nil
	})
}

func UpsertIBCDenoms(db *gorm.DB, denoms []models.IBCDenom) error {
	return db.Transaction(func(dbTransaction *gorm.DB) error {
		for i := range denoms {
			if err := dbTransaction.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "hash"}},
				DoUpdates: clause.AssignmentColumns([]string{"path", "base_denom"}),
			}).Create(&denoms[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
