//go:build purego

package webhookoutbox

// fullSyncParam asks modernc.org/sqlite for synchronous=FULL; it runs after
// the synchronous(1) that FormatChatStorageURI sets, so it wins.
const fullSyncParam = "_pragma=synchronous(2)"
