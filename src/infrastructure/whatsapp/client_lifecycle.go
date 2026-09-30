package whatsapp

import (
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

// GetClient returns the current global client instance (alias for GetGlobalClient)
func GetClient() *whatsmeow.Client {
	globalStateMu.RLock()
	defer globalStateMu.RUnlock()
	return cli
}

func getStoreContainers() (*sqlstore.Container, *sqlstore.Container) {
	globalStateMu.RLock()
	defer globalStateMu.RUnlock()
	return db, keysDB
}

// InitializeDeviceManager creates the global DeviceManager if it doesn't exist.
func InitializeDeviceManager(storeContainer, keysStoreContainer *sqlstore.Container, chatStorageRepo domainChatStorage.IChatStorageRepository) *DeviceManager {
	globalStateMu.Lock()
	defer globalStateMu.Unlock()
	if deviceManager == nil {
		deviceManager = NewDeviceManager(storeContainer, keysStoreContainer, chatStorageRepo)
	}
	return deviceManager
}

// GetDeviceManager returns the global DeviceManager.
func GetDeviceManager() *DeviceManager {
	globalStateMu.RLock()
	defer globalStateMu.RUnlock()
	return deviceManager
}

// SharedLIDStore is the LID map shared by every device of the gateway (whatsmeow keeps one
// table for all of them), for callers that have no connected client, such as a device that was
// created and never paired. Nil when the store is not initialized.
func SharedLIDStore() store.LIDStore {
	container, _ := getStoreContainers()
	if container == nil || container.LIDMap == nil {
		return nil
	}
	return container.LIDMap
}
