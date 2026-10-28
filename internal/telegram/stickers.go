package telegram

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/dnwandana/tether-agent/internal/db"
)

// StickerInfo is the cached description of a static sticker (Hermes sticker_cache.json).
type StickerInfo struct {
	Description string `json:"description"`
	Emoji       string `json:"emoji"`
	SetName     string `json:"set_name"`
}

// StickerCache keeps sticker descriptions by file_unique_id.
type StickerCache interface {
	Get(ctx context.Context, uniqueID string) (StickerInfo, bool)
	Put(ctx context.Context, uniqueID string, info StickerInfo)
}

// KVStickers is the StickerCache in the kv table of state.db. The cache has no limit and
// no expiry, the same as Hermes.
type KVStickers struct {
	DB  *db.DB
	Log *slog.Logger
}

const stickerKeyPrefix = "telegram.sticker."

// Get returns the cached description of a sticker. A read error or a bad value is a miss,
// so that a broken row does not stop a turn.
func (k *KVStickers) Get(ctx context.Context, uniqueID string) (StickerInfo, bool) {
	key := stickerKeyPrefix + uniqueID
	raw, ok, err := k.DB.KVGet(ctx, key)
	if err != nil {
		k.logger().Warn("telegram: cannot read the sticker cache", "key", key, "err", err)
		return StickerInfo{}, false
	}
	if !ok {
		return StickerInfo{}, false
	}
	var info StickerInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		k.logger().Warn("telegram: the sticker cache has a bad value; ignore it", "key", key, "err", err)
		return StickerInfo{}, false
	}
	return info, true
}

// Put stores the description of a sticker. A write error only logs, because the cache is
// not necessary for a turn.
func (k *KVStickers) Put(ctx context.Context, uniqueID string, info StickerInfo) {
	key := stickerKeyPrefix + uniqueID
	raw, err := json.Marshal(info)
	if err != nil {
		k.logger().Warn("telegram: cannot encode the sticker cache value", "key", key, "err", err)
		return
	}
	if err := k.DB.KVSet(ctx, key, string(raw)); err != nil {
		k.logger().Warn("telegram: cannot write the sticker cache", "key", key, "err", err)
	}
}

func (k *KVStickers) logger() *slog.Logger {
	if k.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return k.Log
}
