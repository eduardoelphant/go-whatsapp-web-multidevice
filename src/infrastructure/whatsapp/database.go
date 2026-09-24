package whatsapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// InitWaDB initializes the WhatsApp database connection
func InitWaDB(ctx context.Context, DBURI string) *sqlstore.Container {
	log = waLog.Stdout("Main", config.WhatsappLogLevel, true)
	dbLog := waLog.Stdout("Database", config.WhatsappLogLevel, true)

	storeContainer, err := initDatabase(ctx, dbLog, DBURI)
	if err != nil {
		log.Errorf("Database initialization error: %v", err)
		panic(pkgError.InternalServerError(fmt.Sprintf("Database initialization error: %v", err)))
	}

	return storeContainer
}

// initDatabase creates and returns a database store container based on the configured URI
func initDatabase(ctx context.Context, dbLog waLog.Logger, DBURI string) (*sqlstore.Container, error) {
	driver, dsn, err := ResolveDBDriver(DBURI)
	if err != nil {
		return nil, err
	}
	return sqlstore.New(ctx, driver, dsn, dbLog)
}

// ResolveDBDriver maps a --db-uri / --db-keys-uri value to the database/sql
// driver name and DSN used for the whatsmeow store, so that tools opening the
// same store (such as import-baileys) apply identical SQLite pragmas.
func ResolveDBDriver(DBURI string) (driver string, dsn string, err error) {
	// Strip surrounding quotes that may come from .env file parsing
	DBURI = strings.Trim(DBURI, `"'`)

	if strings.HasPrefix(DBURI, "file:") {
		return sqlite.DriverName, sqlite.FormatChatStorageURI(DBURI, true, true), nil
	} else if strings.HasPrefix(DBURI, "postgres:") {
		return "postgres", DBURI, nil
	}

	return "", "", fmt.Errorf("unknown database type: %s. Currently only sqlite3(file:) and postgres are supported", DBURI)
}
