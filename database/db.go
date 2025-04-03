package database

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"

	"x-ui/config"
	"x-ui/database/model"
	"x-ui/xray"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var db *gorm.DB
var dbType string

const (
	defaultUsername = "admin"
	defaultPassword = "admin"
	defaultSecret   = ""
)

func initModels() error {
	models := []any{
		&model.User{},
		&model.Inbound{},
		&model.OutboundTraffics{},
		&model.Setting{},
		&model.InboundClientIps{},
		&xray.ClientTraffic{},
	}
	for _, model := range models {
		if err := db.AutoMigrate(model); err != nil {
			log.Printf("Error auto migrating model: %v", err)
			return err
		}
	}
	return nil
}

func initUser() error {
	empty, err := isTableEmpty("users")
	if err != nil {
		log.Printf("Error checking if users table is empty: %v", err)
		return err
	}
	if empty {
		user := &model.User{
			Username:    defaultUsername,
			Password:    defaultPassword,
			LoginSecret: defaultSecret,
		}
		return db.Create(user).Error
	}
	return nil
}

func isTableEmpty(tableName string) (bool, error) {
	var count int64
	err := db.Table(tableName).Count(&count).Error
	return count == 0, err
}

func InitDB() error {
	dbType = config.GetDBType()

	var dialector gorm.Dialector
	var err error

	switch dbType {
	case "sqlite":
		dbPath := config.GetDBPath()
		dir := path.Dir(dbPath)
		err = os.MkdirAll(dir, fs.ModePerm)
		if err != nil {
			log.Printf("Error creating db directory: %v", err)
			return err
		}
		dialector = sqlite.Open(dbPath)
		log.Printf("Initializing SQLite database at: %s", dbPath)
	case "postgres":
		dsn := config.GetDBDSN()
		if dsn == "" {
			err = errors.New("PostgreSQL DSN (XUI_DB_DSN) is not configured")
			log.Printf("Error: %v", err)
			return err
		}
		dialector = postgres.Open(dsn)
		log.Printf("Initializing PostgreSQL database...")
	default:
		err = fmt.Errorf("unsupported database type: %s", dbType)
		log.Printf("Error: %v", err)
		return err
	}

	var gormLogger logger.Interface

	if config.IsDebug() {
		gormLogger = logger.Default
	} else {
		gormLogger = logger.Discard
	}

	c := &gorm.Config{
		Logger: gormLogger,
	}
	db, err = gorm.Open(dialector, c)
	if err != nil {
		log.Printf("Error opening database: %v", err)
		return err
	}

	if err := initModels(); err != nil {
		return err
	}
	if err := initUser(); err != nil {
		return err
	}

	log.Printf("Database initialization completed successfully.")
	return nil
}

func CloseDB() error {
	if db != nil {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return nil
}

func GetDB() *gorm.DB {
	return db
}

func IsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}

func IsSQLiteDB(file io.ReaderAt) (bool, error) {
	signature := []byte("SQLite format 3\x00")
	buf := make([]byte, len(signature))
	_, err := file.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return false, err
	}
	return bytes.Equal(buf, signature), nil
}

func Checkpoint() error {
	if dbType == "sqlite" {
		log.Printf("Running PRAGMA wal_checkpoint for SQLite...")
		err := db.Exec("PRAGMA wal_checkpoint;").Error
		if err != nil {
			log.Printf("Error running PRAGMA wal_checkpoint: %v", err)
			return err
		}
		log.Printf("PRAGMA wal_checkpoint completed.")
	}
	return nil
}
