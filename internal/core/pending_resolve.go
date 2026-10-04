package core

import (
	"fmt"
	"strings"
)

type pendingPair struct {
	meta map[string]any
	blob PendingBlob
}

func collectPendingPairs(list []map[string]any, pairs []pendingPair) []pendingPair {
	for _, meta := range list {
		id, _ := meta["attachmentId"].(string)
		if !strings.HasPrefix(id, PendingAttachmentPrefix) {
			continue
		}
		blob, ok := TakePendingBlob(id)
		if !ok {
			continue
		}
		pairs = append(pairs, pendingPair{meta: meta, blob: blob})
	}
	return pairs
}

// ResolvePendingAttachments uploads the `pending:…` slots on payload (result
// level and every step of every section) and rewrites them in place with the
// real attachment ids. It returns upload error messages; the caller decides
// whether the result is still worth reporting.
func ResolvePendingAttachments(client *Client, payload *Payload) []string {
	if client == nil || payload == nil {
		return nil
	}

	var pairs []pendingPair
	pairs = collectPendingPairs(payload.Attachments, pairs)
	for _, steps := range [][]StepJSON{payload.Setup, payload.Test, payload.Teardown} {
		for _, step := range steps {
			pairs = collectPendingPairs(step.Attachments, pairs)
		}
	}
	if len(pairs) == 0 {
		return nil
	}

	files := make([]BlobFile, 0, len(pairs))
	for _, pair := range pairs {
		files = append(files, BlobFile{
			FileName: pair.blob.FileName,
			MimeType: pair.blob.MimeType,
			Data:     pair.blob.Data,
			Path:     pair.blob.Path,
		})
	}

	uploaded, err := client.UploadBlobs(files)
	if err != nil {
		LogError("Tracera: %s", err)
		return []string{err.Error()}
	}

	var errs []string
	for i, pair := range pairs {
		var result *BlobUploadResult
		if i < len(uploaded) {
			result = uploaded[i]
		}
		if result == nil || result.ID == "" {
			errs = append(errs, fmt.Sprintf("attachment upload missing result at index %d", i))
			pair.meta["attachmentId"] = ""
			continue
		}
		pair.meta["attachmentId"] = result.ID
		pair.meta["fileName"] = result.FileName
		pair.meta["mimeType"] = result.MimeType
	}
	return errs
}

// discardPendingList drops the registered body behind every `pending:…` slot
// in list. A result that is never reported must not keep its bytes registered.
func discardPendingList(list []map[string]any) {
	for _, meta := range list {
		if id, _ := meta["attachmentId"].(string); strings.HasPrefix(id, PendingAttachmentPrefix) {
			TakePendingBlob(id)
		}
	}
}

// DiscardPendingBlobs drops the bodies behind every pending slot on payload
// (result level and every step). Runners reach it through EnqueueResult when a
// result is dropped (soft-disabled, unbound, reporting stopped).
func DiscardPendingBlobs(payload *Payload) {
	if payload == nil {
		return
	}
	discardPendingList(payload.Attachments)
	for _, steps := range [][]StepJSON{payload.Setup, payload.Test, payload.Teardown} {
		for _, step := range steps {
			discardPendingList(step.Attachments)
		}
	}
}

// discardNodeBlobs drops the bodies behind the pending slots on a step tree.
func discardNodeBlobs(nodes []*stepNode) {
	for _, node := range nodes {
		discardPendingList(node.attachments)
		discardNodeBlobs(node.children)
	}
}
