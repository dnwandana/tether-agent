package app

import (
	"context"
	"errors"
	"strconv"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/telegram"
)

// ownerLine is the line that the group gets when a prompt goes to the owner's private chat.
const ownerLine = "I asked the owner in a private chat."

// ownerPrompter asks the owner in a private chat for a prompt of a group turn. It finds the
// owner chat at the time of the prompt.
type ownerPrompter struct {
	ch       *telegram.Channel
	userID   string
	chatID   string
	threadID string
}

// PromptApproval sends the group line, then asks in the owner's private chat. With no owner
// chat, or when the owner never started a chat with the bot, it returns ErrNoPrivateChat.
func (p *ownerPrompter) PromptApproval(ctx context.Context, req security.ApprovalRequest) (security.Scope, error) {
	owner, ok := p.ch.OwnerChat(ctx, p.userID)
	if !ok {
		return security.ScopeDeny, telegram.ErrNoPrivateChat
	}
	to := gateway.Target{Source: "telegram", ChatID: p.chatID, ThreadID: p.threadID}
	if err := p.ch.Send(ctx, to, gateway.Outbound{Text: ownerLine, Raw: true}); err != nil && ctx.Err() != nil {
		return security.ScopeDeny, ctx.Err()
	}
	if p.ch.UI == nil {
		return security.ScopeDeny, telegram.ErrNoPrivateChat
	}
	// The private chat id of a Telegram user is the user id.
	scope, err := p.ch.UI.For(owner, "", strconv.FormatInt(owner, 10)).PromptApproval(ctx, req)
	var api *telegram.APIError
	if errors.As(err, &api) && api.Code == 403 {
		return security.ScopeDeny, telegram.ErrNoPrivateChat
	}
	return scope, err
}
