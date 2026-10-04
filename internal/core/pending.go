package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PendingAttachmentPrefix marks an attachment slot whose blob is uploaded just
// before the result goes on the wire.
const PendingAttachmentPrefix = "pending:"

const defaultMimeType = "application/octet-stream"

// PendingBlob is the body of an attachment waiting for upload. A blob either
// streams from Path or carries Data — never both, so disk artifacts stay on
// disk until the upload reads them.
type PendingBlob struct {
	FileName string
	MimeType string
	Data     []byte
	Path     string
}

// NewPendingBlob builds an attachment body. Path wins: in-memory data is
// dropped when a path is given.
func NewPendingBlob(fileName, mimeType string, data []byte, path string) PendingBlob {
	blob := PendingBlob{FileName: fileName, MimeType: mimeType}
	if strings.TrimSpace(path) != "" {
		blob.Path = path
		return blob
	}
	blob.Data = data
	return blob
}

var (
	pendingBlobs      sync.Map // pending id -> PendingBlob
	pendingIDFallback atomic.Uint64
)

// NewPendingAttachmentID mints a unique id for an attachment slot.
func NewPendingAttachmentID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s%d-%d", PendingAttachmentPrefix, time.Now().UnixNano(), pendingIDFallback.Add(1))
	}
	return PendingAttachmentPrefix + hex.EncodeToString(raw[:])
}

// RegisterPendingBlob stores the body for a slot id.
func RegisterPendingBlob(id string, blob PendingBlob) { pendingBlobs.Store(id, blob) }

// TakePendingBlob removes and returns the body for a slot id.
func TakePendingBlob(id string) (PendingBlob, bool) {
	v, ok := pendingBlobs.LoadAndDelete(id)
	if !ok {
		return PendingBlob{}, false
	}
	blob, ok := v.(PendingBlob)
	return blob, ok
}

// ClearPendingBlobs drops every registered body (end of run; nothing left to
// report them on).
func ClearPendingBlobs() {
	pendingBlobs.Range(func(key, _ any) bool {
		pendingBlobs.Delete(key)
		return true
	})
}

// PendingBlobCount is how many bodies are still waiting for upload.
func PendingBlobCount() int {
	count := 0
	pendingBlobs.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// resolveAttachmentMime prefers the caller's type, then the file extension.
func resolveAttachmentMime(fileName string, explicit []string) string {
	if len(explicit) > 0 && strings.TrimSpace(explicit[0]) != "" {
		return strings.TrimSpace(explicit[0])
	}
	guessed := mime.TypeByExtension(filepath.Ext(fileName))
	if guessed == "" {
		return defaultMimeType
	}
	if i := strings.IndexByte(guessed, ';'); i >= 0 {
		guessed = strings.TrimSpace(guessed[:i])
	}
	return guessed
}
