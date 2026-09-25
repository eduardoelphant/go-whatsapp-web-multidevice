//go:build !purego

package webhookoutbox

// fullSyncParam asks mattn/go-sqlite3 for synchronous=FULL.
const fullSyncParam = "_synchronous=FULL"
