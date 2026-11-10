// Package extension carries the companion browser extension inside the app,
// so installing Helium Syncer is enough: the app writes these files to its
// own folder and points each synced Helium profile at them.
//
// Helium loads the folder as it is. It ignores this Go file.
package extension

import "embed"

// Files are the extension's files, exactly as Helium loads them.
//
//go:embed manifest.json background.js
var Files embed.FS

// Names lists the files in Files, in a fixed order.
var Names = []string{"manifest.json", "background.js"}
