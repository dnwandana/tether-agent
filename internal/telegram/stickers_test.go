package telegram

import "testing"

func TestKVStickersRoundTrip(t *testing.T) {
	d := openTestDB(t)
	c := &KVStickers{DB: d}
	if _, ok := c.Get(ctx, "u1"); ok {
		t.Fatal("hit on an empty cache")
	}
	c.Put(ctx, "u1", StickerInfo{Description: "A duck laughs", Emoji: "😂", SetName: "DuckPack"})
	got, ok := c.Get(ctx, "u1")
	if !ok || got != (StickerInfo{Description: "A duck laughs", Emoji: "😂", SetName: "DuckPack"}) {
		t.Fatalf("%+v %v", got, ok)
	}
	raw, ok, err := d.KVGet(ctx, "telegram.sticker.u1")
	if err != nil || !ok || raw != `{"description":"A duck laughs","emoji":"😂","set_name":"DuckPack"}` {
		t.Fatalf("%q %v %v", raw, ok, err)
	}
}

func TestKVStickersBadValueIsAMiss(t *testing.T) {
	d := openTestDB(t)
	if err := d.KVSet(ctx, "telegram.sticker.u2", "not json"); err != nil {
		t.Fatal(err)
	}
	if _, ok := (&KVStickers{DB: d}).Get(ctx, "u2"); ok {
		t.Fatal("a bad value must be a miss")
	}
}
