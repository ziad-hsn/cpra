package management

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// CollectionUploadItem borrows the exact frozen resource-object bytes. It must
// not be retained or formatted as input diagnostics. Source is an opaque token,
// never a client path. Caller ownership continues for the duration of Upload.
type CollectionUploadItem struct {
	Ordinal, SourceDocument, SourceItem uint64
	Key                                 persistence.CatalogKey
	Source, ContentDigest               string
	Resource                            []byte
}

func (CollectionUploadItem) String() string               { return "private collection upload (input omitted)" }
func (i CollectionUploadItem) GoString() string           { return i.String() }
func (i CollectionUploadItem) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(i.String())) }
func (CollectionUploadItem) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private upload input cannot be serialized")
}

// UploadCollection commits only inactive ciphertext. Each accepted original row
// advances a durable prefix; an interrupted chunk is resumed from that prefix.
// Whole-collection validation and activation remain separate operations.
// Endpoint and per-resource authorization must stay valid throughout admission.
func (c *Catalog) UploadCollection(ctx context.Context, id, actor string, items []CollectionUploadItem, canWrite func(persistence.CatalogKey) bool, now func() time.Time, admit CollectionCommit) (api.Operation, error) {
	if ctx == nil || admit == nil || canWrite == nil || now == nil || len(items) == 0 || len(items) > 256 {
		return api.Operation{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return api.Operation{}, err
	}
	if err := c.readyContext(ctx); err != nil {
		return api.Operation{}, err
	}
	bytes := 0
	keys := make(map[persistence.CatalogKey]struct{}, len(items))
	for n, item := range items {
		if !canWrite(item.Key) {
			return api.Operation{}, errCollectionReadDenied
		}
		if _, duplicate := keys[item.Key]; duplicate {
			return api.Operation{}, persistence.ErrCollectionConflict
		}
		keys[item.Key] = struct{}{}
		bytes += len(item.Resource)
		if bytes > 4<<20 || n > 0 && item.Ordinal != items[n-1].Ordinal+1 {
			return api.Operation{}, ErrValidation
		}
	}
	receipt, err := c.store.CollectionReceipt(ctx, id, now())
	if err != nil {
		return api.Operation{}, err
	}
	if receipt.Actor != actor {
		return api.Operation{}, persistence.ErrOperationNotFound
	}
	for len(items) > 0 {
		count, err := c.uploadCollectionBatch(ctx, id, actor, items, canWrite, now, admit)
		if err != nil {
			return api.Operation{ID: id}, err
		}
		items = items[count:]
	}
	receipt, err = c.store.CollectionReceipt(ctx, id, now())
	if err != nil {
		return api.Operation{ID: id}, err
	}
	return collectionOperationView(receipt), nil
}

// One existing Raft envelope amortizes log and history-watermark syncs. Rows
// retain their original command semantics: accepted prefixes survive later
// conditional rejection, and a failed response must be reconciled by reading
// the original operation. This is not an atomic or rollback-capable chunk.
func (c *Catalog) uploadCollectionBatch(ctx context.Context, id, actor string, items []CollectionUploadItem, canWrite func(persistence.CatalogKey) bool, now func() time.Time, admit CollectionCommit) (int, error) {
	prepared, err := newCollectionUploadPreparationAs(ctx, c, id, actor, canWrite, now)
	if err != nil {
		return 0, err
	}
	defer prepared.close()
	maxCommands, maxBytes := c.store.CommandLimits()
	maxCommands = min(maxCommands, 256, len(items))
	// Historical ownerless operations cannot carry the committed authority
	// required by UploadFence. Keep their original one-row admission path.
	if prepared.authority == nil {
		maxCommands = 1
	}
	if maxCommands < 1 || maxBytes <= 64 {
		return 0, ErrUnavailable
	}
	fence := persistence.CollectionUploadFence{Uploaded: prepared.head.Uploaded, EncodedBytes: prepared.head.EncodedBytes, ProgressDigest: prepared.head.ProgressDigest}
	if prepared.authority != nil {
		fence.Authority = *prepared.authority
	}
	commands := make([]persistence.Command, 0, maxCommands)
	encodedBytes := 64 // Store.CommandEncodedBound's versioned envelope allowance.
	for _, item := range items[:maxCommands] {
		row, retry, err := prepared.prepareNext(ctx, collectionUploadInput{Ref: stagedItemRef{Ordinal: item.Ordinal, Key: item.Key,
			Source: item.Source, Document: item.SourceDocument, Item: item.SourceItem}, ContentDigest: item.ContentDigest, Resource: item.Resource}, fence.Uploaded+1)
		if err != nil {
			return 0, err
		}
		command := persistence.Command{Kind: "collection", At: now().UTC(), Collection: &persistence.CollectionCommand{
			Action: "upload", OperationID: id, UploadID: prepared.head.UploadID, Item: &row}}
		next := fence
		// Exact retries retain their historical command shape, including a full
		// maximum-size prefix. Public admission holds the policy read lock until
		// synchronous Submit returns; policy replacement requires a stopped
		// administrative store, which cannot accept collection commands.
		if !retry && prepared.authority != nil {
			prior := fence
			command.Collection.UploadFence = &prior
			next, err = persistence.CollectionUploadNextFence(id, prior, row)
			if err != nil {
				return 0, err
			}
		}
		bound, err := persistence.CommandEncodedBound(command)
		if err != nil || bound > maxBytes-64 {
			return 0, ErrValidation
		}
		if bound > maxBytes-encodedBytes {
			break
		}
		commands = append(commands, command)
		encodedBytes += bound
		fence = next
	}
	if len(commands) == 0 {
		return 0, ErrValidation
	}
	var results []persistence.Result
	invoked, submitted := false, false
	err = admit(func() error {
		if invoked {
			return ErrValidation
		}
		invoked = true
		if err := prepared.check(ctx); err != nil {
			return err
		}
		for _, command := range commands {
			if !canWrite(command.Collection.Item.Key) {
				return errCollectionReadDenied
			}
		}
		at := now().UTC()
		for i := range commands {
			commands[i].At = at
		}
		submitted = true
		var submitErr error
		results, submitErr = c.store.Submit(ctx, commands)
		if submitErr != nil {
			return errors.Join(ErrOutcomeUnconfirmed, submitErr)
		}
		return nil
	})
	if err != nil {
		if submitted {
			err = errors.Join(ErrOutcomeUnconfirmed, err)
		}
		return 0, err
	}
	if !invoked {
		return 0, ErrValidation
	}
	if len(results) != len(commands) {
		return 0, ErrOutcomeUnconfirmed
	}
	// Every command in the envelope has already run. Return the first ordered
	// failure without implying that later commands were rolled back or skipped.
	for _, result := range results {
		if result.Err != nil {
			return 0, result.Err
		}
	}
	return len(commands), nil
}
