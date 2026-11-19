package telegram

import (
	"context"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// pairApprove pairs userID on telegram through a code.
func pairApprove(t *testing.T, p *Pairing, userID string) {
	t.Helper()
	code, _, err := p.Request(context.Background(), "telegram", userID, "user"+userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Approve(context.Background(), "telegram", code); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerChatOrder(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	ch := &Channel{DB: d, Pair: &Pairing{DB: d},
		Cfg: config.TelegramConfig{AllowedUsers: []string{"@ana", " 42 ", "43"}, HomeChat: "-100555"}}
	// 1. The user of the turn, when that user is an owner.
	if id, ok := ch.OwnerChat(ctx, "43"); !ok || id != 43 {
		t.Fatalf("got %d %v", id, ok)
	}
	// A member, and a user id that is not a number, go to the next steps. The home chat is a
	// group, so step 3 gives the first numeric allowed_users entry.
	for _, uid := range []string{"77", "@ana", ""} {
		if id, ok := ch.OwnerChat(ctx, uid); !ok || id != 42 {
			t.Fatalf("%q: got %d %v", uid, id, ok)
		}
	}
	// 2. A home channel that is a private chat of an owner wins over allowed_users.
	if err := SetHome(ctx, d, "43", ""); err != nil {
		t.Fatal(err)
	}
	if id, ok := ch.HomeOwnerChat(ctx); !ok || id != 43 {
		t.Fatalf("got %d %v", id, ok)
	}
	// 4. With no numeric allowed_users entry and no private home, the first paired user.
	ch2 := &Channel{DB: openTestDB(t), Cfg: config.TelegramConfig{AllowedUsers: []string{"*"}}}
	ch2.Pair = &Pairing{DB: ch2.DB}
	if _, ok := ch2.HomeOwnerChat(ctx); ok {
		t.Fatal("no owner chat exists yet")
	}
	pairApprove(t, ch2.Pair, "88")
	if id, ok := ch2.HomeOwnerChat(ctx); !ok || id != 88 {
		t.Fatalf("got %d %v", id, ok)
	}
}

func TestIsOwnerPrivateChat(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	ch := &Channel{DB: d, Pair: &Pairing{DB: d},
		Cfg: config.TelegramConfig{AllowedUsers: []string{"42"}, GroupAllowedUsers: []string{"55"}}}
	pairApprove(t, ch.Pair, "88")
	for _, c := range []struct {
		chat int64
		want bool
	}{
		{42, true}, {88, true}, {55, false}, {77, false}, {-1001234, false},
	} {
		if got := ch.IsOwnerPrivateChat(ctx, c.chat); got != c.want {
			t.Errorf("%d: got %v, want %v", c.chat, got, c.want)
		}
	}
	// allow_all_users makes each user an owner for the gate, but not for this test.
	open := &Channel{DB: d, Cfg: config.TelegramConfig{AllowAllUsers: true, AllowedUsers: []string{"*"}}}
	if open.IsOwnerPrivateChat(ctx, 77) {
		t.Fatal("a stranger's private chat is not private")
	}
}

// With "*" or allow_all_users, a member is not an owner for the owner chat. The note, the
// "ob:" button, and the secret cards must not go to the private chat of a member.
func TestOwnerChatWildcardIsNotOwner(t *testing.T) {
	ctx := context.Background()
	for i, cfg := range []config.TelegramConfig{
		{AllowedUsers: []string{"*", "42"}},
		{AllowAllUsers: true, AllowedUsers: []string{"42"}},
	} {
		d := openTestDB(t)
		ch := &Channel{DB: d, Pair: &Pairing{DB: d}, Cfg: cfg}
		if id, ok := ch.OwnerChat(ctx, "77"); !ok || id != 42 {
			t.Fatalf("config %d: a member gets the owner chat 42, got %d %v", i, id, ok)
		}
		// A home channel that is the private chat of a member is not an owner chat.
		if err := SetHome(ctx, d, "77", ""); err != nil {
			t.Fatal(err)
		}
		if id, ok := ch.HomeOwnerChat(ctx); !ok || id != 42 {
			t.Fatalf("config %d: got %d %v", i, id, ok)
		}
		// A paired user stays an owner.
		pairApprove(t, ch.Pair, "88")
		if id, ok := ch.OwnerChat(ctx, "88"); !ok || id != 88 {
			t.Fatalf("config %d: got %d %v", i, id, ok)
		}
	}
}
