// Package baileysimport converts a Baileys (WhiskeySockets, v7) authentication
// state into a whatsmeow device, so that a WhatsApp companion session paired
// through Baileys can continue under whatsmeow without scanning a new QR code.
//
// The flow is Parse (read the JSON dump), Convert (map every key to its
// whatsmeow/go.mau.fi/libsignal form, in memory) and Write (persist the result
// into a whatsmeow SQL store in one transaction). See docs/import-baileys.md
// for the input format, what is imported and what is deliberately dropped.
//
// The package has no dependency on GOWA's application layers; it only needs
// a database/sql handle for the whatsmeow store.
package baileysimport
